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
//
// Identities of real accounts and names that match no account are tracked in
// separate tables. The first is as large as the number of users and is never
// evicted, so flooding with junk names cannot reset a real account's count;
// only the second is bounded, evicting its oldest name when full.
type Slowdown struct {
	mu       sync.Mutex
	free     int
	step     time.Duration
	maxDelay time.Duration
	hardCap  int
	window   time.Duration
	accounts *counterTable
	unknown  *counterTable
	now      func() time.Time
}

// counterTable holds attempt counts by identity, oldest first, which is also
// expiry order, so aging out and eviction take from the front. A maxTracked of
// zero means no bound.
type counterTable struct {
	byIdentity map[string]*list.Element
	order      *list.List
	maxTracked int
}

// tableEntry is one tracked identity.
type tableEntry struct {
	identity string
	entry
}

func newCounterTable(maxTracked int) *counterTable {
	return &counterTable{byIdentity: map[string]*list.Element{}, order: list.New(), maxTracked: maxTracked}
}

// BytesPerUnknownName is what one tracked name costs: the map slot, the list
// element, the entry and the key string.
const BytesPerUnknownName = 170

// minUnknownNames keeps the bound useful on a container with very little memory.
const minUnknownNames = 10_000

// UnknownNameBound is how many unknown names to track: a share of the memory
// limit, in entries, never below a floor. Without a known limit it assumes 512 MiB.
func UnknownNameBound(memoryLimit uint64, share float64) int {
	if memoryLimit == 0 {
		memoryLimit = 512 << 20
	}
	return max(minUnknownNames, int(float64(memoryLimit)*share/BytesPerUnknownName))
}

// NewSlowdown panics on values that make no sense, like New does.
// maxUnknown bounds the table of names that match no account.
func NewSlowdown(free int, step, maxDelay time.Duration, hardCap int, window time.Duration, maxUnknown int) *Slowdown {
	if free < 0 || step <= 0 || maxDelay < step || hardCap <= free || window <= 0 || maxUnknown < 1 {
		panic(fmt.Sprintf("loginguard: slowdown free %d step %s cap %s hard cap %d window %s unknown %d are invalid", free, step, maxDelay, hardCap, window, maxUnknown))
	}
	return &Slowdown{free: free, step: step, maxDelay: maxDelay, hardCap: hardCap, window: window,
		accounts: newCounterTable(0), unknown: newCounterTable(maxUnknown), now: time.Now}
}

// Begin counts an attempt at the identity and says how long it waits before its
// password is checked, or that the account is over its failure cap. known says
// the identity is a real account.
func (s *Slowdown) Begin(identity string, known bool) (delay time.Duration, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := normalise(identity)
	now := s.now()
	s.accounts.expire(now, s.window)
	s.unknown.expire(now, s.window)
	table := s.unknown
	if known {
		table = s.accounts
	}
	e := table.track(key, now)
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
	key := normalise(identity)
	s.accounts.remove(key)
	s.unknown.remove(key)
}

// Failures is how many attempts the identity has counted in the window.
func (s *Slowdown) Failures(identity string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := normalise(identity)
	now := s.now()
	return max(s.accounts.attempts(key, now, s.window), s.unknown.attempts(key, now, s.window))
}

func (t *counterTable) attempts(key string, now time.Time, window time.Duration) int {
	element, ok := t.byIdentity[key]
	if !ok {
		return 0
	}
	e := element.Value.(*tableEntry)
	if now.Sub(e.since) >= window {
		return 0
	}
	return e.attempts
}

// expire drops identities whose window has passed, stopping at the first live
// one: each attempt pays only for what it removes.
func (t *counterTable) expire(now time.Time, window time.Duration) {
	for front := t.order.Front(); front != nil; front = t.order.Front() {
		if now.Sub(front.Value.(*tableEntry).since) < window {
			return
		}
		t.drop(front)
	}
}

// track returns the identity's entry, adding it at the back and, when a bounded
// table is full, evicting the oldest first.
func (t *counterTable) track(key string, now time.Time) *tableEntry {
	if element, ok := t.byIdentity[key]; ok {
		return element.Value.(*tableEntry)
	}
	for t.maxTracked > 0 && t.order.Len() >= t.maxTracked {
		t.drop(t.order.Front())
	}
	e := &tableEntry{identity: key, entry: entry{since: now}}
	t.byIdentity[key] = t.order.PushBack(e)
	return e
}

func (t *counterTable) remove(key string) {
	if element, ok := t.byIdentity[key]; ok {
		t.drop(element)
	}
}

func (t *counterTable) drop(element *list.Element) {
	delete(t.byIdentity, element.Value.(*tableEntry).identity)
	t.order.Remove(element)
}
