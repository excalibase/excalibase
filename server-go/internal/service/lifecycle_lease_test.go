package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// heldClaimer models another replica already running a lifecycle operation
// on the project: this caller never gets the lease.
type heldClaimer struct{ ops []ProjectOperation }

func (c *heldClaimer) Claim(_ context.Context, _ string, op ProjectOperation) (func(), bool, error) {
	c.ops = append(c.ops, op)
	return nil, false, nil
}

// countingClaimer grants the lease and records that it was given back.
type countingClaimer struct {
	mu        sync.Mutex
	claims    int
	releases  int
	claimErr  error
	heldNames []ProjectOperation
}

func (c *countingClaimer) Claim(_ context.Context, _ string, op ProjectOperation) (func(), bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.claimErr != nil {
		return nil, false, c.claimErr
	}
	c.claims++
	c.heldNames = append(c.heldNames, op)
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.releases++
	}, true, nil
}

func (c *countingClaimer) counts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.claims, c.releases
}

// A lifecycle operation on a project another replica is already working on
// must be refused, not interleaved. Interleaving is how a resume starts a
// database that a pause is midway through stopping.
func TestPauseAndResumeRefuseAProjectAnotherOperationHolds(t *testing.T) {
	for name, call := range map[string]func(*observedPauseFixture) error{
		"pause":  func(f *observedPauseFixture) error { return f.pause(t) },
		"resume": func(f *observedPauseFixture) error { return f.svc.Resume(context.Background(), observedPauseProject) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newObservedPause(t)
			if name == "resume" {
				if err := f.pause(t); err != nil {
					t.Fatalf("seed PAUSED: %v", err)
				}
				f.pauser.log = nil
			}
			f.svc.claimer = &heldClaimer{}

			if err := call(f); !errors.Is(err, storage.ErrProjectBusy) {
				t.Fatalf("err: got %v, want ErrProjectBusy", err)
			}
			if len(f.pauser.log) != 0 {
				t.Errorf("a busy project's workload must not be touched: %v", f.pauser.log)
			}
		})
	}
}

// The A/B interleaving the review described: A has stopped the pods but has
// not yet written PAUSED when B is asked to resume. B must be refused; A's
// PAUSED must stand, and the database must not have been started.
func TestAResumeCannotOvertakeAPauseThatHasAlreadyStoppedTheDatabase(t *testing.T) {
	f := newObservedPause(t)
	replicaB := newPauseServiceOver(f)

	// B tries to resume at the moment A is between "pods gone" and "PAUSED
	// written" — A holds the project's lease for the whole run.
	atShutdown := func() {
		if err := replicaB.Resume(context.Background(), observedPauseProject); !errors.Is(err, storage.ErrProjectBusy) {
			t.Errorf("the second replica must be refused, got %v", err)
		}
	}
	f.pauser.onPause = atShutdown

	if err := f.pause(t); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	inst := f.reload(t)
	if inst.Status != string(domain.StatusPaused) {
		t.Errorf("status: got %s, want PAUSED", inst.Status)
	}
	for _, call := range f.pauser.log {
		if call == "resume" {
			t.Fatal("the database must never have been started back up")
		}
	}
}

// The idle sweep's retry takes the same lease, so a manual pause and the
// sweep cannot run against one project at once.
func TestTheIdleSweepRetryTakesTheLease(t *testing.T) {
	f := newObservedPause(t)
	claimer := &countingClaimer{}
	f.svc.claimer = claimer

	if err := f.svc.Pause(context.Background(), observedPauseProject, domain.PauseReasonIdle); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	claims, releases := claimer.counts()
	if claims != 1 || releases != 1 {
		t.Errorf("claims=%d releases=%d, want 1 and 1", claims, releases)
	}
	if len(claimer.heldNames) != 1 || claimer.heldNames[0] != OperationPause {
		t.Errorf("operation: got %v, want pause", claimer.heldNames)
	}
}

// Every exit path gives the lease back — including a step that panics,
// which would otherwise leave the project unusable until the process dies.
func TestTheLeaseIsReleasedOnEveryPath(t *testing.T) {
	cases := map[string]func(*observedPauseFixture){
		"success":       func(*observedPauseFixture) {},
		"backup fails":  func(f *observedPauseFixture) { f.backups.statuses = []string{"FAILED"} },
		"workload fails": func(f *observedPauseFixture) {
			f.pauser.pauseErr = errors.New("hibernate refused")
		},
		"step panics": func(f *observedPauseFixture) {
			f.pauser.onPause = func() { panic("nil map in the pauser") }
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newObservedPause(t)
			claimer := &countingClaimer{}
			f.svc.claimer = claimer
			setup(f)

			func() {
				defer func() { _ = recover() }()
				_ = f.pause(t)
			}()

			claims, releases := claimer.counts()
			if claims != 1 || releases != 1 {
				t.Errorf("claims=%d releases=%d, want the lease given back", claims, releases)
			}
		})
	}
}

func TestAClaimerErrorStopsTheOperation(t *testing.T) {
	f := newObservedPause(t)
	f.svc.claimer = &countingClaimer{claimErr: errors.New("platform db unreachable")}

	if err := f.pause(t); err == nil {
		t.Fatal("a lease that cannot be taken must stop the operation")
	}
	if len(f.pauser.log) != 0 {
		t.Errorf("nothing may run without the lease: %v", f.pauser.log)
	}
}
