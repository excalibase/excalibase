package loginguard

import (
	"fmt"
	"sync"
	"time"
)

// Slowdown makes guessing at one account cost time without ever refusing it:
// after a number of free failures inside a window each further failure adds a
// step to the wait, up to a cap. A correct password still gets in once it has
// waited, so guessing from many addresses cannot lock the owner out; the hard
// per-address limit is the Guard's.
type Slowdown struct {
	mu       sync.Mutex
	free     int
	step     time.Duration
	cap      time.Duration
	window   time.Duration
	failures map[string]*entry
	now      func() time.Time
}

// NewSlowdown panics on values that make no sense, like New does.
func NewSlowdown(free int, step, maxDelay, window time.Duration) *Slowdown {
	if free < 0 || step <= 0 || maxDelay < step || window <= 0 {
		panic(fmt.Sprintf("loginguard: slowdown free %d step %s cap %s window %s are invalid", free, step, maxDelay, window))
	}
	return &Slowdown{free: free, step: step, cap: maxDelay, window: window, failures: map[string]*entry{}, now: time.Now}
}

// Delay is how long the identity's next attempt waits before its password is checked.
func (s *Slowdown) Delay(identity string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.current(normalise(identity))
	if e == nil || e.attempts <= s.free {
		return 0
	}
	return min(s.cap, time.Duration(e.attempts-s.free)*s.step)
}

// Failed counts a wrong password.
func (s *Slowdown) Failed(identity string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := normalise(identity)
	now := s.now()
	for k, e := range s.failures {
		if now.Sub(e.since) >= s.window {
			delete(s.failures, k)
		}
	}
	e, ok := s.failures[key]
	if !ok {
		e = &entry{since: now}
		s.failures[key] = e
	}
	e.attempts++
}

// Succeeded clears the identity's failures after a correct password.
func (s *Slowdown) Succeeded(identity string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.failures, normalise(identity))
}

func (s *Slowdown) current(key string) *entry {
	e, ok := s.failures[key]
	if !ok || s.now().Sub(e.since) >= s.window {
		return nil
	}
	return e
}
