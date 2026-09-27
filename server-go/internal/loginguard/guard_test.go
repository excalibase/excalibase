package loginguard

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type clock struct{ at time.Time }

func (c *clock) now() time.Time { return c.at }

func newTestGuard(limit int, window time.Duration) (*Guard, *clock) {
	c := &clock{at: time.Unix(1_700_000_000, 0)}
	g := New(limit, window)
	g.now = c.now
	return g, c
}

func TestAttemptsBeyondTheLimitAreRefused(t *testing.T) {
	g, _ := newTestGuard(3, time.Minute)
	for i := 1; i <= 3; i++ {
		if !g.Attempt("alice@example.test") {
			t.Fatalf("attempt %d refused inside the budget", i)
		}
	}
	if g.Attempt("alice@example.test") {
		t.Fatal("fourth attempt allowed past a budget of three")
	}
}

func TestTheIdentityIsNormalised(t *testing.T) {
	g, _ := newTestGuard(1, time.Minute)
	g.Attempt("Alice@Example.test")
	if g.Attempt("  alice@example.TEST ") {
		t.Fatal("a case or whitespace variant escaped the budget")
	}
}

func TestIdentitiesHaveSeparateBudgets(t *testing.T) {
	g, _ := newTestGuard(1, time.Minute)
	g.Attempt("alice@example.test")
	if !g.Attempt("bob@example.test") {
		t.Fatal("one identity's failures locked out another")
	}
}

func TestTheBudgetReturnsAfterTheWindow(t *testing.T) {
	g, c := newTestGuard(1, time.Minute)
	g.Attempt("alice@example.test")
	c.at = c.at.Add(time.Minute)
	if !g.Attempt("alice@example.test") {
		t.Fatal("budget did not return once the window passed")
	}
}

func TestSuccessClearsTheCount(t *testing.T) {
	g, _ := newTestGuard(2, time.Minute)
	g.Attempt("alice@example.test")
	g.Succeeded("alice@example.test")
	g.Attempt("alice@example.test")
	if !g.Attempt("alice@example.test") {
		t.Fatal("a success did not clear earlier failures")
	}
}

func TestExpiredIdentitiesAreForgotten(t *testing.T) {
	g, c := newTestGuard(5, time.Minute)
	for i := 0; i < 100; i++ {
		g.Attempt(fmt.Sprintf("user%d@example.test", i))
	}
	c.at = c.at.Add(2 * time.Minute)
	g.Attempt("late@example.test")
	if n := g.size(); n != 1 {
		t.Fatalf("tracked identities after the window: got %d, want 1", n)
	}
}

// Concurrent guesses for one identity cannot all slip in before any is counted.
func TestConcurrentAttemptsNeverExceedTheLimit(t *testing.T) {
	g, _ := newTestGuard(5, time.Minute)
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if g.Attempt("alice@example.test") {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got != 5 {
		t.Fatalf("allowed %d concurrent attempts, want exactly 5", got)
	}
}

func TestNewRefusesAnEmptyBudget(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New accepted a zero limit")
		}
	}()
	New(0, time.Minute)
}
