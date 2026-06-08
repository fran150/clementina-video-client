package mia

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"sort"
	"sync"
	"time"
)

type ClientConfig struct {
	ServerAddress     string
	BindAddress       string
	RequestFPS        int
	RepairTimeout     time.Duration
	NoResponseRetries int
}

type Stats struct {
	SessionID        uint32
	LastFrameID      uint32
	RequestID        uint16
	AppliedFrames    uint64
	NoDirtyResponses uint64
	RepairsSent      uint64
	Reconnects       uint64
	LastDirtyPages   int
	LastStatus       string
}

type Client struct {
	cfg ClientConfig

	conn   *net.UDPConn
	server *net.UDPAddr
	stop   chan struct{}
	done   chan struct{}

	mirrorMu sync.RWMutex
	mirror   []byte

	statsMu sync.RWMutex
	stats   Stats

	sessionID           uint32
	seq                 uint32
	lastPeerSeq         uint32
	lastCompleteFrameID uint32
	nextRequestID       uint16
	nextRequestAt       time.Time
	nextHelloAt         time.Time
	requestInterval     time.Duration
	pending             *pendingResponse
}

type pendingResponse struct {
	requestID              uint16
	lastCompleteFrameID    uint32
	frameID                uint32
	chunkCount             uint16
	chunks                 map[uint16][]byte
	deadline               time.Time
	retriesWithoutResponse int
}

func NewClient(cfg ClientConfig) *Client {
	return &Client{
		cfg:             cfg,
		stop:            make(chan struct{}),
		done:            make(chan struct{}),
		mirror:          make([]byte, VideoStateSize),
		nextRequestID:   1,
		requestInterval: time.Second / time.Duration(cfg.RequestFPS),
		stats: Stats{
			LastStatus: "starting",
		},
	}
}

func (c *Client) Start() error {
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

	return nil
}

func (c *Client) Close() {
	select {
	case <-c.done:
		return
	default:
	}

	close(c.stop)
	if c.conn != nil {
		_ = c.conn.Close()
	}
	<-c.done
}

func (c *Client) Snapshot(dst []byte) []byte {
	if dst == nil || len(dst) != VideoStateSize {
		dst = make([]byte, VideoStateSize)
	}

	c.mirrorMu.RLock()
	copy(dst, c.mirror)
	c.mirrorMu.RUnlock()

	return dst
}

func (c *Client) Stats() Stats {
	c.statsMu.RLock()
	defer c.statsMu.RUnlock()

	return c.stats
}

func (c *Client) run() {
	defer close(c.done)

	c.sendHello("startup")
	buf := make([]byte, UDPPayloadSize)

	for {
		select {
		case <-c.stop:
			return
		default:
		}

		timeout := c.nextTimeout(time.Now())
		_ = c.conn.SetReadDeadline(time.Now().Add(timeout))
		n, _, err := c.conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				c.serviceTimers(time.Now())
				continue
			}
			return
		}

		packet, err := ParsePacket(buf[:n])
		if err != nil {
			log.Printf("discarding malformed MIA packet: %v", err)
			c.sendStatus(StatusProtocolErr, 0, 0)
			c.sendHello("malformed response")
			continue
		}

		c.lastPeerSeq = packet.Header.Seq
		c.handlePacket(packet)
		c.serviceTimers(time.Now())
	}
}

func (c *Client) nextTimeout(now time.Time) time.Duration {
	const idleTimeout = 25 * time.Millisecond

	timeout := idleTimeout
	if c.sessionID == 0 {
		return timeout
	}
	if c.pending != nil && c.pending.deadline.After(now) {
		remaining := c.pending.deadline.Sub(now)
		if remaining < timeout {
			timeout = remaining
		}
	}
	if c.pending == nil && c.nextRequestAt.After(now) {
		remaining := c.nextRequestAt.Sub(now)
		if remaining < timeout {
			timeout = remaining
		}
	}
	if timeout <= 0 {
		return time.Millisecond
	}

	return timeout
}

func (c *Client) serviceTimers(now time.Time) {
	if c.sessionID == 0 {
		if now.After(c.nextHelloAt) {
			c.sendHello("retry")
		}
		return
	}

	if c.pending == nil {
		if now.Before(c.nextRequestAt) {
			return
		}
		c.startRequest(now)
		return
	}

	if now.Before(c.pending.deadline) {
		return
	}

	if c.pending.frameID == 0 {
		c.pending.retriesWithoutResponse++
		if c.pending.retriesWithoutResponse >= c.cfg.NoResponseRetries {
			c.sendHello("no response")
			return
		}
		c.sendRequest(c.pending.requestID, c.pending.lastCompleteFrameID)
		c.pending.deadline = now.Add(c.cfg.RepairTimeout)
		return
	}

	missing := c.missingChunks()
	if len(missing) == 0 {
		return
	}
	c.sendNack(c.pending.requestID, c.pending.frameID, missing)
	c.pending.deadline = now.Add(c.cfg.RepairTimeout)
	c.updateStats(func(stats *Stats) {
		stats.RepairsSent++
		stats.LastStatus = "repair requested"
	})
}

func (c *Client) handlePacket(packet Packet) {
	switch packet.Header.Type {
	case PacketWelcome:
		c.handleWelcome(packet)
	case PacketFrameData:
		c.handleFrameData(packet)
	case PacketStatus:
		c.handleStatus(packet)
	default:
		c.sendStatus(StatusProtocolErr, packet.Header.RequestID, packet.Header.FrameID)
		c.sendHello("unexpected packet")
	}
}

func (c *Client) handleWelcome(packet Packet) {
	if len(packet.Payload) != 0 || packet.Header.SessionID == 0 {
		c.sendHello("bad welcome")
		return
	}

	c.sessionID = packet.Header.SessionID
	c.lastCompleteFrameID = 0
	c.pending = nil
	c.nextRequestAt = time.Now()
	c.updateStats(func(stats *Stats) {
		stats.SessionID = c.sessionID
		stats.LastFrameID = 0
		stats.LastStatus = "welcome"
	})
}

func (c *Client) handleFrameData(packet Packet) {
	if c.pending == nil || packet.Header.RequestID != c.pending.requestID {
		return
	}
	if packet.Header.SessionID != c.sessionID {
		return
	}
	if err := validateFrameChunkHeader(packet.Header, packet.Payload); err != nil {
		log.Printf("protocol error: %v", err)
		c.sendStatus(StatusProtocolErr, packet.Header.RequestID, packet.Header.FrameID)
		c.sendHello("bad frame chunk")
		return
	}

	if c.pending.frameID == 0 {
		c.pending.frameID = packet.Header.FrameID
		c.pending.chunkCount = packet.Header.ChunkCount
		c.pending.chunks = make(map[uint16][]byte, packet.Header.ChunkCount)
	} else if c.pending.frameID != packet.Header.FrameID || c.pending.chunkCount != packet.Header.ChunkCount {
		c.sendStatus(StatusProtocolErr, packet.Header.RequestID, packet.Header.FrameID)
		c.sendHello("chunk metadata changed")
		return
	}

	payload := make([]byte, len(packet.Payload))
	copy(payload, packet.Payload)
	c.pending.chunks[packet.Header.ChunkIndex] = payload
	c.pending.deadline = time.Now().Add(c.cfg.RepairTimeout)

	if len(c.pending.chunks) != int(c.pending.chunkCount) {
		return
	}

	dirtyPages, err := c.applyPendingResponse()
	if err != nil {
		log.Printf("protocol error: %v", err)
		c.sendStatus(StatusProtocolErr, c.pending.requestID, c.pending.frameID)
		c.sendHello("bad response")
		return
	}

	frameID := c.pending.frameID
	requestID := c.pending.requestID
	c.lastCompleteFrameID = frameID
	c.sendAck(requestID, frameID)
	c.pending = nil
	c.nextRequestAt = time.Now().Add(c.requestInterval)
	c.updateStats(func(stats *Stats) {
		stats.AppliedFrames++
		stats.LastFrameID = frameID
		stats.LastDirtyPages = dirtyPages
		stats.LastStatus = "frame applied"
	})
}

func (c *Client) handleStatus(packet Packet) {
	if len(packet.Payload) != 2 {
		c.sendStatus(StatusProtocolErr, packet.Header.RequestID, packet.Header.FrameID)
		c.sendHello("bad status")
		return
	}

	code := binary.LittleEndian.Uint16(packet.Payload[0:2])
	switch code {
	case StatusNoDirtyPages:
		if c.pending != nil && packet.Header.RequestID != c.pending.requestID {
			return
		}
		c.pending = nil
		c.nextRequestAt = time.Now().Add(c.requestInterval)
		c.updateStats(func(stats *Stats) {
			stats.NoDirtyResponses++
			stats.LastDirtyPages = 0
			stats.LastStatus = "no dirty pages"
		})
	case StatusProtocolErr:
		c.sendHello("server protocol error")
	default:
		c.sendStatus(StatusProtocolErr, packet.Header.RequestID, packet.Header.FrameID)
		c.sendHello("unknown status")
	}
}

func (c *Client) startRequest(now time.Time) {
	requestID := c.nextRequestID
	c.nextRequestID++
	if c.nextRequestID == 0 {
		c.nextRequestID = 1
	}

	c.pending = &pendingResponse{
		requestID:           requestID,
		lastCompleteFrameID: c.lastCompleteFrameID,
		deadline:            now.Add(c.cfg.RepairTimeout),
	}
	c.sendRequest(requestID, c.lastCompleteFrameID)
	c.updateStats(func(stats *Stats) {
		stats.RequestID = requestID
		stats.LastStatus = "request sent"
	})
}

func (c *Client) sendHello(reason string) {
	c.sessionID = 0
	c.lastCompleteFrameID = 0
	c.pending = nil
	c.nextHelloAt = time.Now().Add(time.Second)
	c.send(Header{Type: PacketHello})
	c.updateStats(func(stats *Stats) {
		stats.Reconnects++
		stats.SessionID = 0
		stats.LastFrameID = 0
		stats.LastStatus = "hello: " + reason
	})
}

func (c *Client) sendRequest(requestID uint16, lastCompleteFrameID uint32) {
	payload := make([]byte, 4)
	binary.LittleEndian.PutUint32(payload[0:4], lastCompleteFrameID)
	c.send(Header{
		Type:      PacketRequestFrame,
		SessionID: c.sessionID,
		RequestID: requestID,
	}, payload)
}

func (c *Client) sendAck(requestID uint16, frameID uint32) {
	c.send(Header{
		Type:      PacketAckResponse,
		SessionID: c.sessionID,
		FrameID:   frameID,
		RequestID: requestID,
	})
}

func (c *Client) sendNack(requestID uint16, frameID uint32, missing []uint16) {
	payload := make([]byte, 4+len(missing)*2)
	binary.LittleEndian.PutUint16(payload[0:2], uint16(len(missing)))
	for i, chunk := range missing {
		binary.LittleEndian.PutUint16(payload[4+i*2:6+i*2], chunk)
	}
	c.send(Header{
		Type:      PacketNackChunks,
		SessionID: c.sessionID,
		FrameID:   frameID,
		RequestID: requestID,
	}, payload)
}

func (c *Client) sendStatus(code uint16, requestID uint16, frameID uint32) {
	c.send(Header{
		Type:      PacketStatus,
		SessionID: c.sessionID,
		FrameID:   frameID,
		RequestID: requestID,
	}, StatusPayload(code))
}

func (c *Client) send(header Header, payload ...[]byte) {
	var body []byte
	if len(payload) > 0 {
		body = payload[0]
	}
	c.seq++
	if c.seq == 0 {
		c.seq = 1
	}
	header.Seq = c.seq
	header.Ack = c.lastPeerSeq
	packet := BuildPacket(header, body)
	_, _ = c.conn.WriteToUDP(packet, c.server)
}

func (c *Client) missingChunks() []uint16 {
	if c.pending == nil || c.pending.frameID == 0 {
		return nil
	}

	missing := make([]uint16, 0)
	for chunk := uint16(0); chunk < c.pending.chunkCount; chunk++ {
		if _, ok := c.pending.chunks[chunk]; !ok {
			missing = append(missing, chunk)
		}
	}

	return missing
}

func (c *Client) applyPendingResponse() (int, error) {
	pending := c.pending
	if pending == nil {
		return 0, fmt.Errorf("no pending response")
	}

	chunks := make([]uint16, 0, len(pending.chunks))
	for chunk := range pending.chunks {
		chunks = append(chunks, chunk)
	}
	sort.Slice(chunks, func(i, j int) bool { return chunks[i] < chunks[j] })

	expected := uint16(0)
	lastPage := uint16(0)
	updates := make([]pageUpdate, 0)
	for _, chunk := range chunks {
		if chunk != expected {
			return 0, fmt.Errorf("missing chunk %d", expected)
		}
		payload := pending.chunks[chunk]
		for offset := 0; offset < len(payload); offset += PageRecordSize {
			page := binary.LittleEndian.Uint16(payload[offset : offset+2])
			if page < FirstSyncPage || page >= PageCount {
				return 0, fmt.Errorf("page index out of range: %d", page)
			}
			if page <= lastPage {
				return 0, fmt.Errorf("page index is not strictly increasing")
			}
			lastPage = page
			data := make([]byte, PageSize)
			copy(data, payload[offset+2:offset+PageRecordSize])
			updates = append(updates, pageUpdate{page: page, data: data})
		}
		expected++
	}

	c.mirrorMu.Lock()
	for _, update := range updates {
		start := int(update.page) << PageShift
		validLen := PageSize
		if start+validLen > VideoStateSize {
			validLen = VideoStateSize - start
		}
		copy(c.mirror[start:start+validLen], update.data[:validLen])
	}
	c.mirrorMu.Unlock()

	return len(updates), nil
}

type pageUpdate struct {
	page uint16
	data []byte
}

func validateFrameChunkHeader(header Header, payload []byte) error {
	if header.FrameID == 0 {
		return fmt.Errorf("frame data has frame_id zero")
	}
	if header.ChunkCount == 0 || header.ChunkIndex >= header.ChunkCount {
		return fmt.Errorf("invalid chunk index/count")
	}
	if len(payload) == 0 || len(payload)%PageRecordSize != 0 {
		return fmt.Errorf("frame payload has invalid size")
	}
	if len(payload) > MaxFramePayloadBytes {
		return fmt.Errorf("frame payload is too large")
	}
	recordCount := len(payload) / PageRecordSize
	if recordCount > RecordsPerChunk {
		return fmt.Errorf("too many records in chunk")
	}

	return nil
}

func (c *Client) updateStats(update func(*Stats)) {
	c.statsMu.Lock()
	update(&c.stats)
	c.statsMu.Unlock()
}
