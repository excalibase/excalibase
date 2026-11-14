package loginguard

import (
	"fmt"
	"testing"
	"time"
)

func newTestSlowdown() (*Slowdown, *clock) {
	c := &clock{at: time.Unix(1_700_000_000, 0)}
	s := NewSlowdown(10, 100*time.Millisecond, 2*time.Second, 50, time.Minute, 1000)
	s.now = c.now
	return s, c
}

func begin(s *Slowdown, identity string, times int) (last time.Duration, ok bool) {
	for i := 0; i < times; i++ {
		last, ok = s.Begin(identity, true)
	}
	return last, ok
}

func TestSlowdown_NoDelayWithinTheFreeAttempts(t *testing.T) {
	s, _ := newTestSlowdown()
	if d, ok := begin(s, "alice", 11); d != 0 || !ok {
		t.Fatalf("delay = %s ok=%v inside the free attempts", d, ok)
	}
}

func TestSlowdown_GrowsPerAttemptThenCaps(t *testing.T) {
	s, _ := newTestSlowdown()
	if d, _ := begin(s, "alice", 14); d != 300*time.Millisecond {
		t.Fatalf("delay = %s, want 300ms three attempts past the free ones", d)
	}
	if d, _ := begin(s, "alice", 30); d != 2*time.Second {
		t.Fatalf("delay = %s, want the 2s cap", d)
	}
}

func TestSlowdown_RefusesPastTheHardCapUntilTheWindowPasses(t *testing.T) {
	s, c := newTestSlowdown()
	if _, ok := begin(s, "alice", 50); !ok {
		t.Fatal("refused under the cap")
	}
	if _, ok := s.Begin("alice", true); ok {
		t.Fatal("allowed past the hard cap")
	}
	if got := s.Failures("alice"); got != 50 {
		t.Fatalf("failures = %d, want 50", got)
	}
	c.at = c.at.Add(2 * time.Minute)
	if _, ok := s.Begin("alice", true); !ok {
		t.Fatal("still refused after the window")
	}
}

func TestSlowdown_SuccessClearsTheCount(t *testing.T) {
	s, _ := newTestSlowdown()
	begin(s, "bob", 40)
	s.Succeeded("bob")
	if d, ok := s.Begin("bob", true); d != 0 || !ok || s.Failures("bob") != 1 {
		t.Fatalf("after a success: delay %s ok=%v failures %d", d, ok, s.Failures("bob"))
	}
}

func TestSlowdown_RejectsNonsense(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("a hard cap at or below the free attempts must panic")
		}
	}()
	NewSlowdown(10, time.Second, time.Second, 10, time.Minute, 1000)
}

func TestSlowdown_IsPerAccount(t *testing.T) {
	s, _ := newTestSlowdown()
	begin(s, "alice", 50)
	if _, ok := s.Begin("carol", true); !ok {
		t.Fatal("one account's flood refused another")
	}
}

func TestSlowdown_UnknownNamesAreBoundedAndEvictOldestFirst(t *testing.T) {
	s, c := newTestSlowdown()
	s.unknown.maxTracked = 100
	for i := 0; i < 500; i++ {
		c.at = c.at.Add(time.Millisecond)
		s.Begin(fmt.Sprintf("name-%d", i), false)
	}
	if s.unknown.order.Len() != 100 || len(s.unknown.byIdentity) != 100 {
		t.Fatalf("tracking %d / %d unknown names, want 100", s.unknown.order.Len(), len(s.unknown.byIdentity))
	}
	if s.Failures("name-0") != 0 || s.Failures("name-499") != 1 {
		t.Fatal("the oldest unknown name was not the one evicted")
	}
}

// A flood of junk usernames must not reset a real account that is at its cap.
func TestSlowdown_JunkNamesNeverEvictARealAccount(t *testing.T) {
	s, c := newTestSlowdown()
	s.unknown.maxTracked = 100
	begin(s, "carol", 50)
	if _, ok := s.Begin("carol", true); ok {
		t.Fatal("setup: the account should be at its cap")
	}
	for i := 0; i < 5000; i++ {
		c.at = c.at.Add(time.Millisecond)
		s.Begin(fmt.Sprintf("junk-%d", i), false)
	}
	if _, ok := s.Begin("carol", true); ok {
		t.Fatal("the account was allowed again after a flood of unknown names")
	}
	if got := s.Failures("carol"); got != 50 {
		t.Fatalf("the account's count is %d, want it kept at 50", got)
	}
}

func TestSlowdown_AgesOutOnlyWhatHasExpired(t *testing.T) {
	s, c := newTestSlowdown()
	s.Begin("old", true)
	c.at = c.at.Add(40 * time.Second)
	s.Begin("recent", true)
	c.at = c.at.Add(30 * time.Second)
	s.Begin("trigger", true)
	if s.Failures("old") != 0 || s.Failures("recent") != 1 || len(s.accounts.byIdentity) != 2 {
		t.Fatalf("old %d recent %d tracked %d", s.Failures("old"), s.Failures("recent"), len(s.accounts.byIdentity))
	}
}

// Begin must not cost more as the table grows: live entries are not scanned.
func TestSlowdown_BeginDoesNotScanTheTable(t *testing.T) {
	s, _ := newTestSlowdown()
	for i := 0; i < 200_000; i++ {
		s.Begin(fmt.Sprintf("user-%d", i), true)
	}
	start := time.Now()
	for i := 0; i < 2000; i++ {
		s.Begin("probe", true)
	}
	if per := time.Since(start) / 2000; per > 100*time.Microsecond {
		t.Fatalf("a Begin took %s with 200k tracked, want it independent of the table size", per)
	}
}
