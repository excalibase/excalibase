// Package loginguard bounds password guesses per account, independent of the
// address they come from, so rotating IPs does not buy more guesses.
package loginguard

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type entry struct {
	attempts int
	since    time.Time
}

// Guard allows `limit` sign-in attempts per identity in a fixed window that
// starts at the first attempt. A success clears the identity's count.
type Guard struct {
	mu        sync.Mutex
	limit     int
	window    time.Duration
	entries   map[string]*entry
	lastSweep time.Time
	now       func() time.Time
}

// New panics on a non-positive limit or window: a guard that allows nothing
// or forgets nothing is a wiring mistake, not a policy.
func New(limit int, window time.Duration) *Guard {
	if limit < 1 || window <= 0 {
		panic(fmt.Sprintf("loginguard: limit %d and window %s must be positive", limit, window))
	}
	return &Guard{limit: limit, window: window, entries: map[string]*entry{}, now: time.Now}
}

// Attempt counts a sign-in attempt and reports whether it may proceed. It is
// counted before the password is checked, so concurrent guesses cannot all
// pass before the first failure lands.
func (g *Guard) Attempt(identity string) bool {
	key := normalise(identity)
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	g.sweep(now)
	e, ok := g.entries[key]
	if !ok || now.Sub(e.since) >= g.window {
		e = &entry{since: now}
		g.entries[key] = e
	}
	if e.attempts >= g.limit {
		return false
	}
	e.attempts++
	return true
}

// Succeeded clears the identity's count after a correct password.
func (g *Guard) Succeeded(identity string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.entries, normalise(identity))
}

func (g *Guard) sweep(now time.Time) {
	if now.Sub(g.lastSweep) < g.window {
		return
	}
	g.lastSweep = now
	for key, e := range g.entries {
		if now.Sub(e.since) >= g.window {
			delete(g.entries, key)
		}
	}
}

func (g *Guard) size() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.entries)
}

func normalise(identity string) string {
	return strings.ToLower(strings.TrimSpace(identity))
}
