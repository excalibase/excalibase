package loginguard

import (
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
