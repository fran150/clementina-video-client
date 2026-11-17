package mia

import (
	"testing"
	"time"
)

func TestRTTEstimatorStartsAtMax(t *testing.T) {
	e := newRTTEstimator(100 * time.Millisecond)
	if got := e.Timeout(); got != 100*time.Millisecond {
		t.Fatalf("Timeout() = %v, want 100ms", got)
	}
}

func TestRTTEstimatorSteadyLinkUsesMinimum(t *testing.T) {
	e := newRTTEstimator(100 * time.Millisecond)
	for i := 0; i < 50; i++ {
		e.Sample(5 * time.Millisecond)
	}
	if got := e.Timeout(); got != MinRepairTimeout {
		t.Fatalf("Timeout() = %v, want %v", got, MinRepairTimeout)
	}
}

func TestRTTEstimatorJitterRaisesTimeout(t *testing.T) {
	e := newRTTEstimator(100 * time.Millisecond)
	for i := 0; i < 50; i++ {
		if i%2 == 0 {
			e.Sample(5 * time.Millisecond)
		} else {
			e.Sample(45 * time.Millisecond)
		}
	}
	got := e.Timeout()
	if got <= 50*time.Millisecond || got > 100*time.Millisecond {
		t.Fatalf("Timeout() = %v, want between 30ms and 100ms", got)
	}
}

func TestRTTEstimatorClampsToMax(t *testing.T) {
	e := newRTTEstimator(100 * time.Millisecond)
	e.Sample(300 * time.Millisecond)
	if got := e.Timeout(); got != 100*time.Millisecond {
		t.Fatalf("Timeout() = %v, want 100ms", got)
	}
}

func TestRTTEstimatorBackoffDoublesToMax(t *testing.T) {
	e := newRTTEstimator(100 * time.Millisecond)
	for i := 0; i < 50; i++ {
		e.Sample(5 * time.Millisecond)
	}
	want := []time.Duration{40, 80, 100, 100, 100}
	for retries, w := range want {
		if got := e.Backoff(retries); got != w*time.Millisecond {
			t.Fatalf("Backoff(%d) = %v, want %v", retries, got, w*time.Millisecond)
		}
	}
}
