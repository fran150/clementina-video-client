package mia

import (
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeInputServer stands in for the MIA input endpoint, recording what the
// client sends and optionally answering it.
type fakeInputServer struct {
	conn *net.UDPConn
	done chan struct{}

	mu      sync.Mutex
	packets []InputPacket
	reply   func(InputPacket) []byte
}

func newFakeInputServer(t *testing.T) *fakeInputServer {
	t.Helper()

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	server := &fakeInputServer{conn: conn, done: make(chan struct{})}
	go server.run()
	t.Cleanup(func() {
		_ = conn.Close()
		<-server.done
	})

	return server
}

func (s *fakeInputServer) run() {
	defer close(s.done)

	buf := make([]byte, 512)
	for {
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}

		packet, err := ParseInputPacket(buf[:n])
		if err != nil {
			continue
		}

		s.mu.Lock()
		s.packets = append(s.packets, packet)
		reply := s.reply
		s.mu.Unlock()

		if reply != nil {
			if response := reply(packet); response != nil {
				_, _ = s.conn.WriteToUDP(response, addr)
			}
		}
	}
}

func (s *fakeInputServer) address() string {
	return s.conn.LocalAddr().String()
}

func (s *fakeInputServer) setReply(reply func(InputPacket) []byte) {
	s.mu.Lock()
	s.reply = reply
	s.mu.Unlock()
}

func (s *fakeInputServer) countOfType(packetType uint8) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	count := 0
	for _, packet := range s.packets {
		if packet.Header.Type == packetType {
			count++
		}
	}
	return count
}

func welcomeReply(status uint8, session uint32) func(InputPacket) []byte {
	return func(packet InputPacket) []byte {
		if packet.Header.Type != InputPacketHello {
			return nil
		}
		payload := make([]byte, 7)
		payload[0] = status
		binary.LittleEndian.PutUint32(payload[1:5], session)
		binary.LittleEndian.PutUint16(payload[5:7], InputCapText|InputCapKeyboard)
		return BuildInputPacket(InputHeader{Type: InputPacketWelcome}, payload)
	}
}

func startTestInputClient(t *testing.T, server *fakeInputServer) *InputClient {
	t.Helper()

	client := NewInputClient(InputClientConfig{
		Enabled:       true,
		ServerAddress: server.address(),
		BindAddress:   "127.0.0.1:0",
		Capabilities:  InputCapText | InputCapKeyboard,
		RetryInterval: 20 * time.Millisecond,
	})
	if err := client.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(client.Close)

	return client
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

// A silent server stands in for MIA being unreachable at startup: the client
// must keep offering HELLOs instead of giving up after the first one.
func TestInputClientRetriesHelloWhileDisconnected(t *testing.T) {
	server := newFakeInputServer(t)
	startTestInputClient(t, server)

	waitFor(t, "repeated hellos", func() bool {
		return server.countOfType(InputPacketHello) >= 3
	})
}

func TestInputClientStopsRetryingOnceConnected(t *testing.T) {
	server := newFakeInputServer(t)
	server.setReply(welcomeReply(InputWelcomeAccepted, 0x1234))
	client := startTestInputClient(t, server)

	waitFor(t, "connected state", client.Connected)

	settled := server.countOfType(InputPacketHello)
	time.Sleep(100 * time.Millisecond)
	if got := server.countOfType(InputPacketHello); got != settled {
		t.Fatalf("hello count grew while connected: %d -> %d", settled, got)
	}
}

// A busy MIA (input owned by another mode) must not stall the client forever:
// it keeps asking, and connects as soon as MIA starts accepting.
func TestInputClientKeepsRetryingWhenBusy(t *testing.T) {
	server := newFakeInputServer(t)
	server.setReply(welcomeReply(InputWelcomeBusy, 0))
	client := startTestInputClient(t, server)

	waitFor(t, "busy state", func() bool {
		return client.Stats().State == InputStateBusy
	})
	waitFor(t, "retries while busy", func() bool {
		return server.countOfType(InputPacketHello) >= 3
	})

	server.setReply(welcomeReply(InputWelcomeAccepted, 0x4321))
	waitFor(t, "connection after MIA frees the input", client.Connected)
}

// A MIA reset wipes the input session without telling the client. The video
// client's fresh welcome is the signal to re-announce, which is what
// ClientConfig.OnSessionEstablished wires up.
func TestInputClientReconnectsOnRequestConnect(t *testing.T) {
	server := newFakeInputServer(t)
	server.setReply(welcomeReply(InputWelcomeAccepted, 0x1234))
	client := startTestInputClient(t, server)

	waitFor(t, "connected state", client.Connected)
	before := server.countOfType(InputPacketHello)

	server.setReply(welcomeReply(InputWelcomeAccepted, 0x5678))
	client.RequestConnect()

	waitFor(t, "hello after reset", func() bool {
		return server.countOfType(InputPacketHello) > before
	})
	waitFor(t, "new session", func() bool {
		return client.Stats().SessionID == 0x5678
	})
}

func TestVideoClientNotifiesOnWelcome(t *testing.T) {
	notified := make(chan struct{}, 1)
	client := NewClient(ClientConfig{
		RequestFPS: 25,
		OnSessionEstablished: func() {
			notified <- struct{}{}
		},
	})

	client.handleWelcome(Packet{Header: Header{Type: PacketWelcome, SessionID: 0x99}})

	select {
	case <-notified:
	default:
		t.Fatal("welcome did not notify the session listener")
	}
	if client.sessionID != 0x99 {
		t.Fatalf("session id = %#x, want 0x99", client.sessionID)
	}
}
