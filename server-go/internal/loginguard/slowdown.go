package loginguard

import (
	"container/list"
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
	// failures is by identity; order holds the same entries oldest first, which
	// is also expiry order, so aging out and eviction take from the front.
	failures   map[string]*list.Element
	order      *list.List
	maxTracked int
	now        func() time.Time
}

// slowdownEntry is one tracked identity.
type slowdownEntry struct {
	identity string
	entry
}

// defaultMaxTracked bounds the table: names that are not accounts create
// entries too, and at about 150 bytes each this is a few tens of megabytes.
const defaultMaxTracked = 200_000

// NewSlowdown panics on values that make no sense, like New does.
func NewSlowdown(free int, step, maxDelay time.Duration, hardCap int, window time.Duration) *Slowdown {
	if free < 0 || step <= 0 || maxDelay < step || hardCap <= free || window <= 0 {
		panic(fmt.Sprintf("loginguard: slowdown free %d step %s cap %s hard cap %d window %s are invalid", free, step, maxDelay, hardCap, window))
	}
	return &Slowdown{free: free, step: step, maxDelay: maxDelay, hardCap: hardCap, window: window,
		failures: map[string]*list.Element{}, order: list.New(), maxTracked: defaultMaxTracked, now: time.Now}
}

// Begin counts an attempt at the identity and says how long it waits before its
// password is checked, or that the account is over its failure cap.
func (s *Slowdown) Begin(identity string) (delay time.Duration, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := normalise(identity)
	now := s.now()
	s.expire(now)
	e := s.track(key, now)
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
	if element, ok := s.failures[normalise(identity)]; ok {
		s.drop(element)
	}
}

// Failures is how many attempts the identity has counted in the window.
func (s *Slowdown) Failures(identity string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	element, ok := s.failures[normalise(identity)]
	if !ok {
		return 0
	}
	e := element.Value.(*slowdownEntry)
	if s.now().Sub(e.since) >= s.window {
		return 0
	}
	return e.attempts
}

// expire drops identities whose window has passed. They sit oldest first, so
// this stops at the first live one: each attempt pays only for what it removes.
func (s *Slowdown) expire(now time.Time) {
	for front := s.order.Front(); front != nil; front = s.order.Front() {
		if now.Sub(front.Value.(*slowdownEntry).since) < s.window {
			return
		}
		s.drop(front)
	}
}

// track returns the identity's entry, adding it at the back and, when the table
// is full, evicting the oldest first.
func (s *Slowdown) track(key string, now time.Time) *slowdownEntry {
	if element, ok := s.failures[key]; ok {
		return element.Value.(*slowdownEntry)
	}
	for s.order.Len() >= s.maxTracked {
		s.drop(s.order.Front())
	}
	e := &slowdownEntry{identity: key, entry: entry{since: now}}
	s.failures[key] = s.order.PushBack(e)
	return e
}

func (s *Slowdown) drop(element *list.Element) {
	delete(s.failures, element.Value.(*slowdownEntry).identity)
	s.order.Remove(element)
}
