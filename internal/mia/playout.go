package mia

import (
	"sync"
	"time"
)

// playout is an optional jitter buffer between the network and the screen.
// A completed frame is held until delay after its request was sent and then
// copied to the picture the renderer reads. Requests leave on a fixed
// cadence, so frames are shown on that cadence too, even when their replies
// arrive unevenly. A frame that arrives later than its slot is shown at once:
// the buffer hides delays shorter than itself, not longer stalls, and it
// cannot bring back frames MIA never built.
type playout struct {
	delay time.Duration

	mu      sync.Mutex
	queue   []queuedFrame
	lastDue time.Time
	display []byte
	shown   uint64
	late    uint64
}

type queuedFrame struct {
	due     time.Time
	updates []pageUpdate
}

func newPlayout(delay time.Duration) *playout {
	return &playout{delay: delay, display: make([]byte, VideoStateSize)}
}

// push queues a frame whose request was first sent at sentAt. Frames keep
// their order: a frame is never due before the one queued ahead of it.
func (p *playout) push(sentAt, now time.Time, updates []pageUpdate) {
	due := sentAt.Add(p.delay)
	p.mu.Lock()
	defer p.mu.Unlock()
	if due.Before(p.lastDue) {
		due = p.lastDue
	}
	if !due.After(now) {
		p.late++
	}
	p.lastDue = due
	p.queue = append(p.queue, queuedFrame{due: due, updates: updates})
}

// snapshot applies every frame that is due at now and copies the picture.
func (p *playout) snapshot(now time.Time, dst []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for n < len(p.queue) && !p.queue[n].due.After(now) {
		applyUpdates(p.display, p.queue[n].updates)
		n++
	}
	if n > 0 {
		p.shown += uint64(n)
		copy(p.queue, p.queue[n:])
		for i := len(p.queue) - n; i < len(p.queue); i++ {
			p.queue[i] = queuedFrame{}
		}
		p.queue = p.queue[:len(p.queue)-n]
	}
	copy(dst, p.display)
}

// stats returns the frames waiting, the frames shown, and the frames that
// arrived after their slot.
func (p *playout) stats() (queued int, shown, late uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.queue), p.shown, p.late
}

func applyUpdates(state []byte, updates []pageUpdate) {
	for _, update := range updates {
		start := int(update.page) << PageShift
		validLen := PageSize
		if start+validLen > VideoStateSize {
			validLen = VideoStateSize - start
		}
		copy(state[start:start+validLen], update.data[:validLen])
	}
}
