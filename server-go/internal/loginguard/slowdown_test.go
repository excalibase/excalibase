package loginguard

import (
	"fmt"
	"testing"
	"time"
)

func newTestSlowdown() (*Slowdown, *clock) {
	c := &clock{at: time.Unix(1_700_000_000, 0)}
	s := NewSlowdown(10, 100*time.Millisecond, 2*time.Second, 50, time.Minute)
	s.now = c.now
	return s, c
}

func begin(s *Slowdown, identity string, times int) (last time.Duration, ok bool) {
	for i := 0; i < times; i++ {
		last, ok = s.Begin(identity)
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
	if _, ok := s.Begin("alice"); ok {
		t.Fatal("allowed past the hard cap")
	}
	if got := s.Failures("alice"); got != 50 {
		t.Fatalf("failures = %d, want 50", got)
	}
	c.at = c.at.Add(2 * time.Minute)
	if _, ok := s.Begin("alice"); !ok {
		t.Fatal("still refused after the window")
	}
}

func TestSlowdown_SuccessClearsTheCount(t *testing.T) {
	s, _ := newTestSlowdown()
	begin(s, "bob", 40)
	s.Succeeded("bob")
	if d, ok := s.Begin("bob"); d != 0 || !ok || s.Failures("bob") != 1 {
		t.Fatalf("after a success: delay %s ok=%v failures %d", d, ok, s.Failures("bob"))
	}
}

func TestSlowdown_RejectsNonsense(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("a hard cap at or below the free attempts must panic")
		}
	}()
	NewSlowdown(10, time.Second, time.Second, 10, time.Minute)
}

func TestSlowdown_IsPerAccount(t *testing.T) {
	s, _ := newTestSlowdown()
	begin(s, "alice", 50)
	if _, ok := s.Begin("carol"); !ok {
		t.Fatal("one account's flood refused another")
	}
}

func TestSlowdown_TableIsBoundedAndEvictsTheOldest(t *testing.T) {
	s, c := newTestSlowdown()
	s.maxTracked = 100
	begin(s, "first", 3)
	for i := 0; i < 500; i++ {
		c.at = c.at.Add(time.Millisecond)
		s.Begin(fmt.Sprintf("name-%d", i))
	}
	if got := s.order.Len(); got != 100 || len(s.failures) != 100 {
		t.Fatalf("tracking %d (list) / %d (map) identities, want 100", got, len(s.failures))
	}
	if s.Failures("first") != 0 {
		t.Fatal("the oldest identity was not the one evicted")
	}
	if s.Failures("name-499") != 1 {
		t.Fatal("the newest identity was evicted")
	}
}

func TestSlowdown_AgesOutOnlyWhatHasExpired(t *testing.T) {
	s, c := newTestSlowdown()
	s.Begin("old")
	c.at = c.at.Add(40 * time.Second)
	s.Begin("recent")
	c.at = c.at.Add(30 * time.Second)
	s.Begin("trigger")
	if s.Failures("old") != 0 || s.Failures("recent") != 1 || len(s.failures) != 2 {
		t.Fatalf("old %d recent %d tracked %d", s.Failures("old"), s.Failures("recent"), len(s.failures))
	}
}

// Begin must not cost more as the table grows: a large table of live entries is not scanned.
func TestSlowdown_BeginDoesNotScanTheTable(t *testing.T) {
	s, _ := newTestSlowdown()
	s.maxTracked = 200_000
	for i := 0; i < 200_000; i++ {
		s.Begin(fmt.Sprintf("name-%d", i))
	}
	start := time.Now()
	for i := 0; i < 2000; i++ {
		s.Begin("probe")
	}
	if per := time.Since(start) / 2000; per > 100*time.Microsecond {
		t.Fatalf("a Begin took %s with 200k tracked, want it independent of the table size", per)
	}
}
