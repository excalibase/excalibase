package planbudget

import (
	"context"
	"testing"
)

type seen struct {
	calls []string
	order *[]string
}

func (s *seen) observe(_ context.Context, _ Querier, orgID string) {
	s.calls = append(s.calls, orgID)
	if s.order != nil {
		*s.order = append(*s.order, "observer")
	}
}

func TestAnObserverSeesTheOrgAfterTheLockAndBeforeTheStepIsDecided(t *testing.T) {
	db, script := openScript(t, okLock, tierIs("FREE"), databases(), apps())
	gate := gateOn()
	var steps []string
	watcher := &seen{order: &steps}
	gate.SetObserver(watcher.observe)
	delta := func(context.Context) (Size, error) {
		steps = append(steps, "delta")
		return Size{}, nil
	}
	if err := gate.Admit(context.Background(), db, "org-1", delta); err != nil {
		t.Fatal(err)
	}
	if len(watcher.calls) != 1 || watcher.calls[0] != "org-1" {
		t.Fatalf("the observer sees the org once, got %v", watcher.calls)
	}
	if len(steps) != 2 || steps[0] != "observer" || steps[1] != "delta" {
		t.Fatalf("the observer runs before the step's size is read, got %v", steps)
	}
	if !script.askedAbout("pg_advisory_xact_lock") {
		t.Fatal("the lock comes first")
	}
}

func TestAnObserverSeesAShrinkingStepToo(t *testing.T) {
	db, _ := openScript(t, okLock)
	gate := gateOn()
	watcher := &seen{}
	gate.SetObserver(watcher.observe)
	if err := gate.Admit(context.Background(), db, "org-1", grows(Size{CPUMilli: -500})); err != nil {
		t.Fatal(err)
	}
	if len(watcher.calls) != 1 {
		t.Fatalf("a pause is the step the sample must catch: %v", watcher.calls)
	}
}

func TestAnInactiveGateNeverCallsTheObserver(t *testing.T) {
	db, _ := openScript(t)
	watcher := &seen{}
	off := NewGate(func(context.Context) bool { return false }, tinyPlans)
	off.SetObserver(watcher.observe)
	if err := off.Admit(context.Background(), db, "org-1", grows(Size{CPUMilli: 1})); err != nil {
		t.Fatal(err)
	}
	if len(watcher.calls) != 0 {
		t.Fatalf("nothing is observed while the budget is off: %v", watcher.calls)
	}
}
