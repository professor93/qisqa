// Per-IP limiter against guessing: an address that asks for too many codes
// that do not exist is blocked for a while. All state is in memory.
package main

import (
	"sync"
	"time"
)

// strikeWindow is the period over which strikes are counted.
const strikeWindow = time.Minute

// Limiter counts "strikes" per client IP. Safe for concurrent use.
type Limiter struct {
	mu      sync.Mutex
	clients map[string]*client
}

// client is the state kept for one IP address.
type client struct {
	windowStart  time.Time // when the current counting window began
	strikes      int       // strikes inside the current window
	blockedUntil time.Time // zero when the address was never blocked
}

// NewLimiter returns a limiter with no recorded clients.
func NewLimiter() *Limiter {
	return &Limiter{clients: make(map[string]*client)}
}

// Strike records one bad request from ip at now. More than perMinute
// strikes within one window block the address for blockFor. The limits are
// passed on every call so that a config reload applies immediately.
func (l *Limiter) Strike(ip string, now time.Time, perMinute int, blockFor time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.clients[ip]
	if c == nil {
		c = &client{windowStart: now}
		l.clients[ip] = c
	}
	if now.Sub(c.windowStart) >= strikeWindow {
		c.windowStart, c.strikes = now, 0
	}
	c.strikes++
	if c.strikes > perMinute {
		c.blockedUntil = now.Add(blockFor)
	}
}

// Blocked reports whether ip is blocked at now.
func (l *Limiter) Blocked(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.clients[ip]
	return c != nil && now.Before(c.blockedUntil)
}

// Sweep forgets clients that are neither blocked nor inside an open window,
// so the map does not grow without bound.
func (l *Limiter) Sweep(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for ip, c := range l.clients {
		if !now.Before(c.blockedUntil) && now.Sub(c.windowStart) >= strikeWindow {
			delete(l.clients, ip)
		}
	}
}

// Len returns the number of clients currently remembered.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.clients)
}
