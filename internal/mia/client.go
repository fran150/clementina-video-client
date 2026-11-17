package mia

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"os"
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
	LogFrames         bool
	// PlayoutDelay, when above zero, holds each frame until this long after
	// its request was sent before showing it (see playout.go).
	PlayoutDelay time.Duration

	// OnSessionEstablished runs on the client goroutine every time MIA answers a
	// HELLO with a new video session. A MIA reset drops the video and the input
	// session together, so this doubles as the reset signal the input client
	// needs to re-announce itself.
	OnSessionEstablished func()
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
	// RepairTimeout is the quiet time the client currently waits before a
	// retry or NACK, and SmoothedRTT the round trip it is based on.
	RepairTimeout time.Duration
	SmoothedRTT   time.Duration
	// Playout buffer, when enabled: frames waiting, frames shown, and frames
	// that arrived after their slot.
	PlayoutDelay  time.Duration
	PlayoutQueued int
	PlayoutShown  uint64
	PlayoutLate   uint64
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
	lastRequestStart    time.Time
	nextHelloAt         time.Time
	requestInterval     time.Duration
	pending             *pendingResponse
	rtt                 rttEstimator
	playout             *playout
	frameLog            *os.File
}

type pendingResponse struct {
	requestID              uint16
	lastCompleteFrameID    uint32
	frameID                uint32
	chunkCount             uint16
	chunks                 map[uint16][]byte
	deadline               time.Time
	retriesWithoutResponse int
	// sentAt is when the request was first sent; the first reply to an
	// unretried request gives a round-trip sample (a reply to a retried one
	// could answer either copy, so it is not measured).
	sentAt   time.Time
	measured bool
	nacks    int
}

// slowResponseLog is how long a request may take, from first send to its
// complete reply, before the client logs it as a visible stall.
const slowResponseLog = 250 * time.Millisecond

// logIfSlow reports a response that held the picture still long enough to
// notice, with what the client did meanwhile, so a freeze can be matched to
// lost requests, lost chunks or a link that went quiet.
func (c *Client) logIfSlow(p *pendingResponse, what string) {
	took := time.Since(p.sentAt)
	if took < slowResponseLog {
		return
	}
	log.Printf("video: slow %s: %s after the request (%d request retries, %d NACKs, %d of %d chunks, repair timeout %s)",
		what, took.Round(time.Millisecond), p.retriesWithoutResponse, p.nacks,
		len(p.chunks), p.chunkCount, c.rtt.Timeout())
}

func NewClient(cfg ClientConfig) *Client {
	return &Client{
		cfg:             cfg,
		stop:            make(chan struct{}),
		done:            make(chan struct{}),
		mirror:          make([]byte, VideoStateSize),
		nextRequestID:   1,
		requestInterval: time.Second / time.Duration(cfg.RequestFPS),
		rtt:             newRTTEstimator(cfg.RepairTimeout),
		playout:         newPlayoutIfEnabled(cfg.PlayoutDelay),
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

	if c.cfg.LogFrames {
		f, err := os.OpenFile("frame_updates.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("failed to open frame log: %w", err)
		}
		c.frameLog = f
	}

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
	if c.frameLog != nil {
		_ = c.frameLog.Close()
	}
	<-c.done
}

func (c *Client) Snapshot(dst []byte) []byte {
	if dst == nil || len(dst) != VideoStateSize {
		dst = make([]byte, VideoStateSize)
	}

	if c.playout != nil {
		c.playout.snapshot(time.Now(), dst)
		return dst
	}

	c.mirrorMu.RLock()
	copy(dst, c.mirror)
	c.mirrorMu.RUnlock()

	return dst
}

func newPlayoutIfEnabled(delay time.Duration) *playout {
	if delay <= 0 {
		return nil
	}
	return newPlayout(delay)
}

func (c *Client) Stats() Stats {
	c.statsMu.RLock()
	stats := c.stats
	c.statsMu.RUnlock()

	if c.playout != nil {
		stats.PlayoutDelay = c.playout.delay
		stats.PlayoutQueued, stats.PlayoutShown, stats.PlayoutLate = c.playout.stats()
	}
	return stats
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
			select {
			case <-c.stop:
				return
			default:
			}
			// Any other read error (the Mac's Wi-Fi dropping or changing
			// network, an ICMP error) used to end this loop for good: no
			// more requests or HELLOs, so the screen froze until the client
			// was restarted, and resetting the computer could not help.
			// Log it and keep going; the timers reconnect when MIA answers.
			log.Printf("video: read from MIA failed: %v", err)
			time.Sleep(10 * time.Millisecond)
			c.serviceTimers(time.Now())
			continue
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
		// The reconnect point stays at NoResponseRetries x the configured
		// timeout (1 s by default) however short the adaptive retries are.
		giveUp := time.Duration(c.cfg.NoResponseRetries) * c.cfg.RepairTimeout
		if now.Sub(c.pending.sentAt) >= giveUp {
			c.sendHello("no response")
			return
		}
		c.pending.retriesWithoutResponse++
		c.sendRequest(c.pending.requestID, c.pending.lastCompleteFrameID)
		c.pending.deadline = now.Add(c.rtt.Backoff(c.pending.retriesWithoutResponse))
		return
	}

	missing := c.missingChunks()
	if len(missing) == 0 {
		return
	}
	c.sendNack(c.pending.requestID, c.pending.frameID, missing)
	c.pending.nacks++
	c.pending.deadline = now.Add(c.rtt.Timeout())
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

	if c.cfg.OnSessionEstablished != nil {
		c.cfg.OnSessionEstablished()
	}
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

	c.measureRTT()
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
	c.pending.deadline = time.Now().Add(c.rtt.Timeout())

	if len(c.pending.chunks) != int(c.pending.chunkCount) {
		return
	}

	updates, err := c.applyPendingResponse()
	if err != nil {
		log.Printf("protocol error: %v", err)
		c.sendStatus(StatusProtocolErr, c.pending.requestID, c.pending.frameID)
		c.sendHello("bad response")
		return
	}

	frameID := c.pending.frameID
	requestID := c.pending.requestID

	if c.frameLog != nil {
		fmt.Fprintf(c.frameLog, "%s: applied frame %d (%d pages)\n", time.Now().Format(time.RFC3339), frameID, len(updates))
		for _, u := range updates {
			fmt.Fprintf(c.frameLog, "  page %04d: %x\n", u.page, u.data)
		}
	}

	c.logIfSlow(c.pending, "frame")
	c.lastCompleteFrameID = frameID
	c.sendAck(requestID, frameID)
	c.pending = nil
	c.scheduleNextRequest()
	c.updateStats(func(stats *Stats) {
		stats.AppliedFrames++
		stats.LastFrameID = frameID
		stats.LastDirtyPages = len(updates)
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
		if c.pending != nil {
			c.measureRTT()
			c.logIfSlow(c.pending, "no-change reply")
		}
		c.pending = nil
		c.scheduleNextRequest()
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

	c.lastRequestStart = now
	c.pending = &pendingResponse{
		requestID:           requestID,
		lastCompleteFrameID: c.lastCompleteFrameID,
		deadline:            now.Add(c.rtt.Timeout()),
		sentAt:              now,
	}
	c.sendRequest(requestID, c.lastCompleteFrameID)
	c.updateStats(func(stats *Stats) {
		stats.RequestID = requestID
		stats.LastStatus = "request sent"
	})
}

// measureRTT feeds the first reply to an unretried request into the round-trip
// estimate that sets the repair timeout.
func (c *Client) measureRTT() {
	p := c.pending
	if p == nil || p.measured {
		return
	}
	p.measured = true
	if p.retriesWithoutResponse != 0 {
		return
	}
	c.rtt.Sample(time.Since(p.sentAt))
	timeout, srtt := c.rtt.Timeout(), c.rtt.SmoothedRTT()
	c.updateStats(func(stats *Stats) {
		stats.RepairTimeout = timeout
		stats.SmoothedRTT = srtt
	})
}

// scheduleNextRequest keeps requests on a fixed cadence measured from when the
// last one started, so the round trip is not added on top of the interval:
// with the interval counted from the reply, a 35 ms round trip turned 25
// requests a second into about 13.
func (c *Client) scheduleNextRequest() {
	next := c.lastRequestStart.Add(c.requestInterval)
	if now := time.Now(); next.Before(now) {
		next = now
	}
	c.nextRequestAt = next
}

func (c *Client) sendHello(reason string) {
	if reason != "startup" && reason != "retry" {
		// Each HELLO costs a full screen refresh; say why it happened.
		log.Printf("video: reconnecting to MIA: %s", reason)
	}
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

func (c *Client) applyPendingResponse() ([]pageUpdate, error) {
	pending := c.pending
	if pending == nil {
		return nil, fmt.Errorf("no pending response")
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
			return nil, fmt.Errorf("missing chunk %d", expected)
		}
		payload := pending.chunks[chunk]
		for offset := 0; offset < len(payload); offset += PageRecordSize {
			page := binary.LittleEndian.Uint16(payload[offset : offset+2])
			if page < FirstSyncPage || page >= PageCount {
				return nil, fmt.Errorf("page index out of range: %d", page)
			}
			if page <= lastPage {
				return nil, fmt.Errorf("page index is not strictly increasing")
			}
			lastPage = page
			data := make([]byte, PageSize)
			copy(data, payload[offset+2:offset+PageRecordSize])
			updates = append(updates, pageUpdate{page: page, data: data})
		}
		expected++
	}

	c.mirrorMu.Lock()
	applyUpdates(c.mirror, updates)
	c.mirrorMu.Unlock()
	if c.playout != nil {
		c.playout.push(pending.sentAt, time.Now(), updates)
	}

	return updates, nil
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
