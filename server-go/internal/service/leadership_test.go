package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/storage"
)

// The cron fires every due project in its own goroutine. They all belong to
// the replica that leads, so every one of them must run — a claim taken per
// job would hand the key to whichever asked first and skip the rest.
func TestLeadershipLetsEveryConcurrentJobRun(t *testing.T) {
	lock := &fakeLeaderLock{}
	leadership := NewLeadership(lock)

	const jobs = 25
	var wg sync.WaitGroup
	results := make([]bool, jobs)
	for i := 0; i < jobs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			leader, err := leadership.IsLeader(context.Background())
			if err != nil {
				t.Errorf("job %d: %v", i, err)
			}
			results[i] = leader
		}(i)
	}
	wg.Wait()

	for i, ran := range results {
		if !ran {
			t.Errorf("job %d was skipped although this replica leads", i)
		}
	}
	if got := lock.acquireCount(); got != 1 {
		t.Errorf("the lock was asked %d times; a standing claim asks once", got)
	}
}

// When another session holds the key this replica fires nothing, quietly.
func TestLeadershipSkipsEveryJobWhenAnotherReplicaLeads(t *testing.T) {
	leadership := NewLeadership(&fakeLeaderLock{refuse: true})
	for i := 0; i < 5; i++ {
		leader, err := leadership.IsLeader(context.Background())
		if err != nil {
			t.Fatalf("a refused claim is not an error: %v", err)
		}
		if leader {
			t.Fatalf("tick %d fired while another replica leads", i)
		}
	}
}

// A claim whose session died is not a claim: the replica stands down and
// asks again rather than firing alongside whoever took over.
func TestLeadershipStandsDownWhenItsLeaseDies(t *testing.T) {
	lock := &fakeLeaderLock{leaseDead: true}
	leadership := NewLeadership(lock)

	for i := 0; i < 3; i++ {
		if leader, err := leadership.IsLeader(context.Background()); err != nil || !leader {
			t.Fatalf("tick %d = %v, %v", i, leader, err)
		}
	}
	if got := lock.acquireCount(); got != 3 {
		t.Errorf("the lock was asked %d times; a dead lease must be re-taken every tick", got)
	}
}

// Standing down hands the key back so another replica need not wait for this
// process's session to be reaped.
func TestLeadershipCloseReleasesTheClaim(t *testing.T) {
	lock := &fakeLeaderLock{}
	leadership := NewLeadership(lock)
	if leader, _ := leadership.IsLeader(context.Background()); !leader {
		t.Fatal("precondition: this replica leads")
	}
	if err := leadership.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if lock.acquired {
		t.Error("the claim was not handed back")
	}
	if err := leadership.Close(context.Background()); err != nil {
		t.Errorf("closing twice must be a no-op, got %v", err)
	}
}

// A lock that cannot be reached is reported, not treated as leadership.
func TestLeadershipReportsLockErrors(t *testing.T) {
	want := errors.New("database unreachable")
	leadership := NewLeadership(erroringLock{err: want})
	leader, err := leadership.IsLeader(context.Background())
	if leader || !errors.Is(err, want) {
		t.Fatalf("IsLeader = %v, %v; want the lock error", leader, err)
	}
}

type erroringLock struct{ err error }

func (l erroringLock) Acquire(context.Context) (storage.LeaderLease, bool, error) {
	return nil, false, l.err
}

// Self-hosted deployments have one replica, so it always leads.
func TestAlwaysLeaderLeads(t *testing.T) {
	leadership := NewLeadership(AlwaysLeader{})
	if leader, err := leadership.IsLeader(context.Background()); err != nil || !leader {
		t.Fatalf("IsLeader = %v, %v", leader, err)
	}
	if err := leadership.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
