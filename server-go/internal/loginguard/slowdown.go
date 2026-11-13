package loginguard

import (
	"fmt"
	"sync"
	"time"
)

// Slowdown makes guessing at one account cost time, and bounds it. Every
// attempt counts as a failure from the moment it begins, so attempts in flight
// together are counted together; a correct password clears the count. After a
// number of free failures inside a window each further attempt waits longer,
// up to a cap, and past a hard failure cap the account refuses attempts until
// the window passes. The wait never refuses a correct password; the hard cap is
// set high enough that spreading it over many addresses is expensive, and the
// Guard's per-address limit is what stops a single address.
type Slowdown struct {
	mu       sync.Mutex
	free     int
	step     time.Duration
	maxDelay time.Duration
	hardCap  int
	window   time.Duration
	failures map[string]*entry
	now      func() time.Time
}

// NewSlowdown panics on values that make no sense, like New does.
func NewSlowdown(free int, step, maxDelay time.Duration, hardCap int, window time.Duration) *Slowdown {
	if free < 0 || step <= 0 || maxDelay < step || hardCap <= free || window <= 0 {
		panic(fmt.Sprintf("loginguard: slowdown free %d step %s cap %s hard cap %d window %s are invalid", free, step, maxDelay, hardCap, window))
	}
	return &Slowdown{free: free, step: step, maxDelay: maxDelay, hardCap: hardCap, window: window,
		failures: map[string]*entry{}, now: time.Now}
}

// Begin counts an attempt at the identity and says how long it waits before its
// password is checked, or that the account is over its failure cap.
func (s *Slowdown) Begin(identity string) (delay time.Duration, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := normalise(identity)
	now := s.now()
	for k, e := range s.failures {
		if now.Sub(e.since) >= s.window {
			delete(s.failures, k)
		}
	}
	e, found := s.failures[key]
	if !found {
		e = &entry{since: now}
		s.failures[key] = e
	}
	if e.attempts >= s.hardCap {
		return 0, false
	}
	if e.attempts > s.free {
		delay = min(s.maxDelay, time.Duration(e.attempts-s.free)*s.step)
	}
	e.attempts++
	return delay, true
}

// Succeeded clears the identity's count after a correct password.
func (s *Slowdown) Succeeded(identity string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.failures, normalise(identity))
}

// Failures is how many attempts the identity has counted in the window.
func (s *Slowdown) Failures(identity string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.failures[normalise(identity)]
	if !ok || s.now().Sub(e.since) >= s.window {
		return 0
	}
	return e.attempts
}
