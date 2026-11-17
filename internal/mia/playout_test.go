package mia

import (
	"testing"
	"time"
)

func page(n uint16, b byte) []pageUpdate {
	data := make([]byte, PageSize)
	for i := range data {
		data[i] = b
	}
	return []pageUpdate{{page: n, data: data}}
}

func TestPlayoutHoldsFrameUntilDue(t *testing.T) {
	p := newPlayout(60 * time.Millisecond)
	t0 := time.Unix(100, 0)
	p.push(t0, t0.Add(15*time.Millisecond), page(FirstSyncPage, 7))

	dst := make([]byte, VideoStateSize)
	off := int(FirstSyncPage) << PageShift
	p.snapshot(t0.Add(50*time.Millisecond), dst)
	if dst[off] != 0 {
		t.Fatalf("frame shown before its slot")
	}
	p.snapshot(t0.Add(60*time.Millisecond), dst)
	if dst[off] != 7 {
		t.Fatalf("frame not shown at its slot")
	}
	if q, shown, late := p.stats(); q != 0 || shown != 1 || late != 0 {
		t.Fatalf("stats = %d %d %d", q, shown, late)
	}
}

func TestPlayoutKeepsOrderAndCountsLate(t *testing.T) {
	p := newPlayout(40 * time.Millisecond)
	t0 := time.Unix(100, 0)
	// First frame is very late; the second, requested 40 ms later, arrives right after.
	p.push(t0, t0.Add(200*time.Millisecond), page(FirstSyncPage, 1))
	p.push(t0.Add(40*time.Millisecond), t0.Add(201*time.Millisecond), page(FirstSyncPage, 2))

	dst := make([]byte, VideoStateSize)
	off := int(FirstSyncPage) << PageShift
	p.snapshot(t0.Add(202*time.Millisecond), dst)
	if dst[off] != 2 {
		t.Fatalf("got %d, want the newer frame applied last", dst[off])
	}
	if _, shown, late := p.stats(); shown != 2 || late != 2 {
		t.Fatalf("shown %d late %d, want 2 and 2", shown, late)
	}
}
