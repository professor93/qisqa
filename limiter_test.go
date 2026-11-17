// Tests for the per-address limiter.
package main

import (
	"testing"
	"time"
)

const (
	testIP       = "203.0.113.7"
	testLimit    = 3
	testBlockFor = 5 * time.Minute
)

// strikes records n strikes for ip at the given time.
func strikes(l *Limiter, ip string, n int, at time.Time) {
	for range n {
		l.Strike(ip, at, testLimit, testBlockFor)
	}
}

func TestLimiterBlocksAfterLimit(t *testing.T) {
	l := NewLimiter()
	strikes(l, testIP, 3, t0)
	eq(t, l.Blocked(testIP, t0), false)

	strikes(l, testIP, 1, t0)
	eq(t, l.Blocked(testIP, t0), true)
	eq(t, l.Blocked(testIP, t0.Add(4*time.Minute+59*time.Second)), true)
	eq(t, l.Blocked(testIP, t0.Add(5*time.Minute)), false)
}

func TestLimiterWindowResets(t *testing.T) {
	l := NewLimiter()
	strikes(l, testIP, 3, t0)
	later := t0.Add(61 * time.Second)
	strikes(l, testIP, 1, later)
	eq(t, l.Blocked(testIP, later), false)
}

func TestLimiterIsPerIP(t *testing.T) {
	l := NewLimiter()
	strikes(l, testIP, 4, t0)
	eq(t, l.Blocked(testIP, t0), true)
	eq(t, l.Blocked("203.0.113.8", t0), false)
}

func TestLimiterSweep(t *testing.T) {
	l := NewLimiter()
	strikes(l, testIP, 1, t0)
	l.Sweep(t0.Add(30 * time.Second))
	eq(t, l.Len(), 1) // its window is still open
	l.Sweep(t0.Add(2 * time.Minute))
	eq(t, l.Len(), 0)

	// A blocked IP is kept until its block ends.
	strikes(l, testIP, 4, t0)
	l.Sweep(t0.Add(2 * time.Minute))
	eq(t, l.Len(), 1)
	eq(t, l.Blocked(testIP, t0.Add(2*time.Minute)), true)
	l.Sweep(t0.Add(6 * time.Minute))
	eq(t, l.Len(), 0)
}
