package mia

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"sync"
)

const (
	InputStateDisabled     = "disabled"
	InputStateDisconnected = "disconnected"
	InputStateHelloSent    = "hello sent"
	InputStateConnected    = "connected"
	InputStateBusy         = "busy"
	InputStateUnsupported  = "unsupported"
	InputStateError        = "error"
)

type InputClientConfig struct {
	Enabled       bool
	ServerAddress string
	BindAddress   string
	Capabilities  uint16
	Name          string
}

type InputStats struct {
	Enabled      bool
	State        string
	SessionID    uint32
	Capabilities uint16
	SentPackets  uint64
	LastError    string
}

type InputClient struct {
	cfg InputClientConfig

	conn   *net.UDPConn
	server *net.UDPAddr
	stop   chan struct{}
	done   chan struct{}

	mu          sync.RWMutex
	state       string
	sessionID   uint32
	seq         uint16
	peerCaps    uint16
	sentPackets uint64
	lastError   string
}

func NewInputClient(cfg InputClientConfig) *InputClient {
	state := InputStateDisconnected
	if !cfg.Enabled {
		state = InputStateDisabled
	}
	if cfg.Name == "" {
		cfg.Name = "clementina-video-client"
	}

	return &InputClient{
		cfg:   cfg,
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
		state: state,
	}
}

func (c *InputClient) Start() error {
	if !c.cfg.Enabled {
		close(c.done)
		return nil
	}

	server, err := net.ResolveUDPAddr("udp4", c.cfg.ServerAddress)
	if err != nil {
		return err
	}
	bind, err := net.ResolveUDPAddr("udp4", c.cfg.BindAddress)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp4", bind)
	if err != nil {
		return err
	}

	c.server = server
	c.conn = conn

	go c.run()
	c.RequestConnect()

	return nil
}

func (c *InputClient) Close() {
	select {
	case <-c.done:
		return
	default:
	}

	c.Disconnect()
	close(c.stop)
	if c.conn != nil {
		_ = c.conn.Close()
	}
	<-c.done
}

func (c *InputClient) Stats() InputStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return InputStats{
		Enabled:      c.cfg.Enabled,
		State:        c.state,
		SessionID:    c.sessionID,
		Capabilities: c.peerCaps,
		SentPackets:  c.sentPackets,
		LastError:    c.lastError,
	}
}

func (c *InputClient) Connected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.sessionID != 0
}

func (c *InputClient) RequestConnect() {
	if !c.cfg.Enabled {
		return
	}

	c.mu.Lock()
	c.sessionID = 0
	c.peerCaps = 0
	c.state = InputStateHelloSent
	c.lastError = ""
	c.mu.Unlock()

	c.sendPacket(InputPacketHello, 0, InputHelloPayload(c.cfg.Capabilities, c.cfg.Name))
}

func (c *InputClient) Disconnect() {
	if !c.cfg.Enabled {
		return
	}

	session := c.currentSession()
	if session != 0 {
		c.sendPacket(InputPacketDisconnect, session, nil)
	}

	c.mu.Lock()
	c.sessionID = 0
	c.peerCaps = 0
	c.state = InputStateDisconnected
	c.mu.Unlock()
}

func (c *InputClient) ClearState(mask uint8) {
	c.sendConnected(InputPacketClearState, []byte{mask})
}

func (c *InputClient) SendText(text []byte) {
	for len(text) > 0 {
		n := len(text)
		if n > 255 {
			n = 255
		}
		c.sendConnected(InputPacketText, InputTextPayload(text[:n]))
		text = text[n:]
	}
}

// SendHIDEvent reports a key press/release. text carries the MIA text byte that
// MIA should enqueue on key-down (0 when the key produces no text, e.g. for keys
// whose text is sent separately via SendText). MIA ignores text on key-up.
func (c *InputClient) SendHIDEvent(usagePage uint16, usageID uint16, down bool, repeat bool, text uint8) {
	var flags uint8
	if down {
		flags |= HIDEventDown
	}
	if repeat {
		flags |= HIDEventRepeat
	}
	c.sendConnected(InputPacketHIDEvent, InputHIDEventPayload(usagePage, usageID, flags, text))
}

func (c *InputClient) SendHIDBitmap(usagePage uint16, bitmap [32]byte) {
	c.sendConnected(InputPacketHIDBitmap, InputHIDBitmapPayload(usagePage, bitmap))
}

func (c *InputClient) SendMouseDelta(buttons uint8, dx int8, dy int8, wheel int8, pan int8) {
	c.sendConnected(InputPacketMouseDelta, InputMouseDeltaPayload(buttons, dx, dy, wheel, pan))
}

func (c *InputClient) SendGamepadState(player uint8, state [10]byte) {
	c.sendConnected(InputPacketGamepadState, InputGamepadStatePayload(player, state))
}

func (c *InputClient) ClearGamepad(player uint8) {
	c.sendConnected(InputPacketGamepadClear, []byte{player})
}

func (c *InputClient) run() {
	defer close(c.done)

	buf := make([]byte, 64)
	for {
		n, _, err := c.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-c.stop:
				return
			default:
			}
			c.setError(err)
			return
		}

		packet, err := ParseInputPacket(buf[:n])
		if err != nil {
			log.Printf("discarding malformed MIA input packet: %v", err)
			continue
		}
		c.handlePacket(packet)
	}
}

func (c *InputClient) handlePacket(packet InputPacket) {
	if packet.Header.Type != InputPacketWelcome {
		return
	}
	if len(packet.Payload) != 7 {
		c.setError(fmt.Errorf("bad input welcome payload size: %d", len(packet.Payload)))
		return
	}

	status := packet.Payload[0]
	session := binary.LittleEndian.Uint32(packet.Payload[1:5])
	caps := binary.LittleEndian.Uint16(packet.Payload[5:7])

	c.mu.Lock()
	defer c.mu.Unlock()

	c.peerCaps = caps
	c.lastError = ""
	switch status {
	case InputWelcomeAccepted:
		if session == 0 {
			c.state = InputStateError
			c.lastError = "accepted input session is zero"
			return
		}
		c.sessionID = session
		c.state = InputStateConnected
	case InputWelcomeBusy:
		c.sessionID = 0
		c.state = InputStateBusy
	case InputWelcomeUnsupportedVersion:
		c.sessionID = 0
		c.state = InputStateUnsupported
	default:
		c.sessionID = 0
		c.state = InputStateError
		c.lastError = fmt.Sprintf("unknown welcome status %d", status)
	}
}

func (c *InputClient) sendConnected(packetType uint8, payload []byte) {
	session := c.currentSession()
	if session == 0 {
		return
	}
	c.sendPacket(packetType, session, payload)
}

func (c *InputClient) currentSession() uint32 {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.sessionID
}

func (c *InputClient) sendPacket(packetType uint8, session uint32, payload []byte) {
	if !c.cfg.Enabled || c.conn == nil {
		return
	}

	c.mu.Lock()
	c.seq++
	if c.seq == 0 {
		c.seq = 1
	}
	seq := c.seq
	c.sentPackets++
	c.mu.Unlock()

	packet := BuildInputPacket(InputHeader{
		Type:    packetType,
		Seq:     seq,
		Session: session,
	}, payload)
	if _, err := c.conn.WriteToUDP(packet, c.server); err != nil {
		c.setError(err)
	}
}

func (c *InputClient) setError(err error) {
	c.mu.Lock()
	c.state = InputStateError
	c.lastError = err.Error()
	c.sessionID = 0
	c.mu.Unlock()
}
