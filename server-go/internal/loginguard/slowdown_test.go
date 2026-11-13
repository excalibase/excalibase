package loginguard

import (
	"testing"
	"time"
)

func newTestSlowdown() (*Slowdown, *clock) {
	c := &clock{at: time.Unix(1_700_000_000, 0)}
	s := NewSlowdown(10, 100*time.Millisecond, 2*time.Second, time.Minute)
	s.now = c.now
	return s, c
}

func TestSlowdown_NoDelayWithinTheFreeFailures(t *testing.T) {
	s, _ := newTestSlowdown()
	for i := 0; i < 10; i++ {
		s.Failed("alice")
	}
	if d := s.Delay("alice"); d != 0 {
		t.Fatalf("delay = %s inside the free failures", d)
	}
}

func TestSlowdown_GrowsPerFailureThenCaps(t *testing.T) {
	s, _ := newTestSlowdown()
	for i := 0; i < 13; i++ {
		s.Failed("alice")
	}
	if d := s.Delay("alice"); d != 300*time.Millisecond {
		t.Fatalf("delay = %s, want 300ms after three failures past the free ones", d)
	}
	for i := 0; i < 500; i++ {
		s.Failed("alice")
	}
	if d := s.Delay("alice"); d != 2*time.Second {
		t.Fatalf("delay = %s, want the 2s cap", d)
	}
}

func TestSlowdown_ForgetsAfterTheWindowAndOnSuccess(t *testing.T) {
	s, c := newTestSlowdown()
	for i := 0; i < 20; i++ {
		s.Failed("alice")
	}
	c.at = c.at.Add(2 * time.Minute)
	if d := s.Delay("alice"); d != 0 {
		t.Fatalf("delay = %s after the window", d)
	}
	for i := 0; i < 20; i++ {
		s.Failed("bob")
	}
	s.Succeeded("bob")
	if d := s.Delay("bob"); d != 0 {
		t.Fatalf("delay = %s after a success", d)
	}
}
