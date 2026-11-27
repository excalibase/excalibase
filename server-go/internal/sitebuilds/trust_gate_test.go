package sitebuilds

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/trust"
)

// EXC-899: a build runs tenant code in a Job, so a limited owner's build never starts.

type stubTrust struct {
	state trust.State
	err   error
}

func (s *stubTrust) ForProject(context.Context, string) (trust.State, error) { return s.state, s.err }

func (b *testBed) gate(state trust.State, err error) *stubTrust {
	owner := &stubTrust{state: state, err: err}
	b.service.options.ContainerGate = trust.NewGate(owner, trust.Policy{MinAgeDays: 90})
	return owner
}

func TestPush_NoBuildRunsForALimitedOwner(t *testing.T) {
	bed := newTestBed(true)
	bed.connectSite()
	bed.connectApp(AppSettings{})
	bed.gate(trust.Limited, nil)
	bed.pushEvent()

	if _, err := bed.service.HandleWebhook(context.Background(), nil, nil); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	bed.nothingStarted(t)
	for _, kind := range []string{KindSite, KindApp} {
		recorded := bed.store.queuedOf(kind)
		if len(recorded) != 1 || recorded[0].Status != StatusFailed || recorded[0].FailureReason == "" {
			t.Errorf("%s: the refused push is recorded as %+v, want one FAILED build saying why", kind, recorded)
		}
	}
}

func TestRebuild_RefusedForALimitedOwner(t *testing.T) {
	bed := newTestBed(true)
	bed.connectSite()
	bed.connectApp(AppSettings{})
	bed.gate(trust.Limited, nil)

	if _, err := bed.service.Rebuild(context.Background(), testProject, testSite, testUser); !errors.Is(err, trust.ErrVerificationRequired) {
		t.Fatalf("Rebuild err = %v, want ErrVerificationRequired", err)
	}
	if _, err := bed.service.RebuildApp(context.Background(), testProject, testApp, testUser); !errors.Is(err, trust.ErrVerificationRequired) {
		t.Fatalf("RebuildApp err = %v, want ErrVerificationRequired", err)
	}
	bed.nothingStarted(t)
	if len(bed.store.builds) != 0 {
		t.Errorf("%d builds recorded, want none", len(bed.store.builds))
	}
}

// A build queued while the owner was verified is stopped before its Job exists once they are limited.
func TestDispatcher_DoesNotStartABuildOfAnOwnerWhoIsNowLimited(t *testing.T) {
	for _, kind := range []string{KindSite, KindApp} {
		bed := newTestBed(true)
		queued := bed.queue(t, kind)
		bed.gate(trust.Limited, nil)

		bed.nothingStarted(t)
		done := bed.store.build(queued.ID)
		if done.Status != StatusFailed || done.FailureReason == "" {
			t.Errorf("%s: build %+v, want FAILED with the verification message", kind, done)
		}
	}
}

func TestBuilds_AVerifiedOwnerRunsAndAnUnreadableCheckFailsClosed(t *testing.T) {
	bed := newTestBed(true)
	bed.connectSite()
	bed.gate(trust.Full, nil)
	if _, err := bed.service.Rebuild(context.Background(), testProject, testSite, testUser); err != nil {
		t.Fatalf("a verified owner: %v", err)
	}
	bed.runAll()
	if len(bed.sites.started()) != 1 {
		t.Fatal("the verified owner's build did not run")
	}

	unreadable := newTestBed(true)
	unreadable.connectSite()
	unreadable.gate(trust.Limited, errors.New("db down"))
	if _, err := unreadable.service.Rebuild(context.Background(), testProject, testSite, testUser); !errors.Is(err, trust.ErrCheckUnavailable) {
		t.Fatalf("err = %v, want ErrCheckUnavailable", err)
	}
	unreadable.nothingStarted(t)
}
