package mia

import "time"

// MinRepairTimeout is the shortest quiet time the client waits before it
// re-sends a request or NACKs missing chunks, however fast the link is. It is
// one frame at 25 fps: with 20 ms, about one request in eight on real
// hardware was re-sent only because its reply was a few ms late.
const MinRepairTimeout = 40 * time.Millisecond

// rttEstimator turns measured request round trips into the quiet time before
// a retry, the way TCP sets its retransmission timeout (RFC 6298): a smoothed
// round trip plus four times its variation, clamped to [MinRepairTimeout, max].
// On a quick, steady link a lost packet is repaired after a few tens of
// milliseconds instead of the fixed maximum; on a jittery one the timeout
// grows so the client does not retry packets that are only late.
type rttEstimator struct {
	max    time.Duration
	srtt   time.Duration
	rttvar time.Duration
	valid  bool
}

func newRTTEstimator(max time.Duration) rttEstimator {
	if max < MinRepairTimeout {
		max = MinRepairTimeout
	}
	return rttEstimator{max: max}
}

func (e *rttEstimator) Sample(rtt time.Duration) {
	if rtt < 0 {
		return
	}
	if !e.valid {
		e.srtt = rtt
		e.rttvar = rtt / 2
		e.valid = true
		return
	}
	diff := e.srtt - rtt
	if diff < 0 {
		diff = -diff
	}
	e.rttvar = (3*e.rttvar + diff) / 4
	e.srtt = (7*e.srtt + rtt) / 8
}

// Timeout is the quiet time before the first retry. Before any sample it is
// the configured maximum.
func (e *rttEstimator) Timeout() time.Duration {
	if !e.valid {
		return e.max
	}
	t := e.srtt + 4*e.rttvar
	if t < MinRepairTimeout {
		t = MinRepairTimeout
	}
	if t > e.max {
		t = e.max
	}
	return t
}

// Backoff is the quiet time after the given number of unanswered retries:
// it doubles each time, up to the configured maximum.
func (e *rttEstimator) Backoff(retries int) time.Duration {
	t := e.Timeout()
	for i := 0; i < retries && t < e.max; i++ {
		t *= 2
	}
	if t > e.max {
		t = e.max
	}
	return t
}

func (e *rttEstimator) SmoothedRTT() time.Duration {
	return e.srtt
}

func (e *rttEstimator) Reset() {
	*e = rttEstimator{max: e.max}
}
