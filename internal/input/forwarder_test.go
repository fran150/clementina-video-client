package input

import (
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/fran150/clementina-video-client/internal/mia"
)

func TestAppendPasteBytes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []byte
	}{
		{"plain ascii", "Hello!", []byte("Hello!")},
		{"lf", "A\nB", []byte{'A', 0x0D, 'B'}},
		{"crlf as one break", "A\r\nB", []byte{'A', 0x0D, 'B'}},
		{"lone cr", "A\rB", []byte{'A', 0x0D, 'B'}},
		{"tab", "A\tB", []byte{'A', 0x09, 'B'}},
		{"drops non-printable and non-ascii", "A\x01\x7FBéC", []byte{'A', 'B', 'C'}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := appendPasteBytes(nil, tc.in)
			if string(got) != string(tc.want) {
				t.Fatalf("appendPasteBytes(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// fakeMIA stands in for the MIA input endpoint: it accepts a HELLO and
// records the bytes carried by every TEXT packet, in order.
type fakeMIA struct {
	conn *net.UDPConn
	done chan struct{}

	mu   sync.Mutex
	text []byte
}

func newFakeMIA(t *testing.T) *fakeMIA {
	t.Helper()

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	server := &fakeMIA{conn: conn, done: make(chan struct{})}
	go server.run()
	t.Cleanup(func() {
		_ = conn.Close()
		<-server.done
	})

	return server
}

func (s *fakeMIA) run() {
	defer close(s.done)

	buf := make([]byte, 512)
	for {
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}

		packet, err := mia.ParseInputPacket(buf[:n])
		if err != nil {
			continue
		}

		switch packet.Header.Type {
		case mia.InputPacketHello:
			payload := make([]byte, 7)
			payload[0] = mia.InputWelcomeAccepted
			binary.LittleEndian.PutUint32(payload[1:5], 0x1234)
			binary.LittleEndian.PutUint16(payload[5:7], mia.InputCapText|mia.InputCapKeyboard)
			response := mia.BuildInputPacket(mia.InputHeader{Type: mia.InputPacketWelcome}, payload)
			_, _ = s.conn.WriteToUDP(response, addr)
		case mia.InputPacketText:
			if len(packet.Payload) < 1 {
				continue
			}
			count := int(packet.Payload[0])
			if count > len(packet.Payload)-1 {
				count = len(packet.Payload) - 1
			}
			s.mu.Lock()
			s.text = append(s.text, packet.Payload[1:1+count]...)
			s.mu.Unlock()
		}
	}
}

func (s *fakeMIA) address() string {
	return s.conn.LocalAddr().String()
}

func (s *fakeMIA) receivedText() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return string(s.text)
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func startConnectedForwarder(t *testing.T, server *fakeMIA, pasteRate float64) (*Forwarder, *mia.InputClient) {
	t.Helper()

	client := mia.NewInputClient(mia.InputClientConfig{
		Enabled:       true,
		ServerAddress: server.address(),
		BindAddress:   "127.0.0.1:0",
		Capabilities:  mia.InputCapText | mia.InputCapKeyboard,
		RetryInterval: 20 * time.Millisecond,
	})
	if err := client.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(client.Close)

	waitFor(t, "connected state", client.Connected)

	return NewForwarder(client, Config{Keyboard: true, PasteRate: pasteRate}), client
}

// A paste is trickled in over time rather than handed to SendText in one
// burst, since MIA's text FIFO is small and has no flow control back to the
// client (see docs/input-protocol.md).
func TestForwarderPasteIsPacedNotBurst(t *testing.T) {
	server := newFakeMIA(t)
	f, _ := startConnectedForwarder(t, server, 40) // 40 bytes/sec => 25ms/byte

	f.QueuePaste("AB\nC") // sanitizes to 4 bytes: A, B, CR, C

	// Immediately after queuing, no time has elapsed for the pacer to admit
	// any bytes yet.
	f.drainPaste()
	if len(f.pasteQueue) != 4 {
		t.Fatalf("pasteQueue len = %d, want 4 (nothing should drain instantly)", len(f.pasteQueue))
	}

	// After enough elapsed time for all 4 bytes at the configured rate, the
	// queue empties and MIA has received exactly the sanitized text.
	time.Sleep(150 * time.Millisecond)
	f.drainPaste()
	if len(f.pasteQueue) != 0 {
		t.Fatalf("pasteQueue len = %d, want 0 after enough elapsed time", len(f.pasteQueue))
	}

	waitFor(t, "pasted text at MIA", func() bool {
		return server.receivedText() == "AB\rC"
	})
}

// A paste queued while disconnected must not be lost or dumped all at once
// the instant a session appears; it waits like any other queued paste.
func TestForwarderPasteWaitsForConnection(t *testing.T) {
	server := newFakeMIA(t)
	client := mia.NewInputClient(mia.InputClientConfig{
		Enabled:       true,
		ServerAddress: server.address(),
		BindAddress:   "127.0.0.1:0",
		Capabilities:  mia.InputCapText | mia.InputCapKeyboard,
		RetryInterval: 20 * time.Millisecond,
	})
	if err := client.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(client.Close)

	f := NewForwarder(client, Config{Keyboard: true, PasteRate: 1000})
	f.QueuePaste("hi")
	f.drainPaste()
	if len(f.pasteQueue) != 2 {
		t.Fatalf("pasteQueue len = %d, want 2 while disconnected", len(f.pasteQueue))
	}

	waitFor(t, "connected state", client.Connected)
	waitFor(t, "queued paste delivered once connected", func() bool {
		f.drainPaste()
		return server.receivedText() == "hi"
	})
}

// ClearAll (window focus loss) drops any in-flight paste instead of letting
// it resume once focus returns.
func TestForwarderClearAllCancelsPaste(t *testing.T) {
	server := newFakeMIA(t)
	f, _ := startConnectedForwarder(t, server, 1)

	f.QueuePaste("abcdef")
	f.ClearAll()

	if len(f.pasteQueue) != 0 {
		t.Fatalf("pasteQueue len = %d, want 0 after ClearAll", len(f.pasteQueue))
	}
}
