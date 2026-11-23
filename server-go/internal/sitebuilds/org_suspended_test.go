package sitebuilds

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/gitprovider"
	"github.com/excalibase/provisioning-poc/internal/orgguard"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

// suspendOrg puts the test project's org under a suspended owner (or lifts it).
func (b *testBed) suspendOrg(suspended *bool) {
	instances := fakestore.NewInstances()
	instances.Items[testProject] = &domain.DatabaseInstance{ProjectID: testProject, OrgID: "org-1"}
	b.service.options.OrgGuard = orgguard.New(instances, func(context.Context) (map[string]bool, error) {
		return map[string]bool{"org-1": *suspended}, nil
	})
}

func (b *testBed) pushEvent() {
	b.provider.event = gitprovider.Event{DeliveryID: "d-1", Push: &gitprovider.Push{InstallationID: testInstall,
		RepositoryID: testRepoID, Repository: testRepo, Branch: "main", Commit: b.provider.head}}
}

func (b *testBed) nothingStarted(t *testing.T) {
	t.Helper()
	b.runAll()
	if sites, apps := b.sites.started(), b.apps.started(); len(sites) != 0 || len(apps) != 0 {
		t.Fatalf("a build ran in a suspended org: %d site runs, %d app runs", len(sites), len(apps))
	}
}

// A member keeps pushing; each push is recorded as refused and never runs.
func TestPush_NoBuildRunsWhileTheOrgOwnerIsSuspended(t *testing.T) {
	bed := newTestBed(true)
	bed.connectSite()
	bed.connectApp(AppSettings{})
	suspended := true
	bed.suspendOrg(&suspended)
	bed.pushEvent()

	if _, err := bed.service.HandleWebhook(context.Background(), nil, nil); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	bed.nothingStarted(t)
	for _, kind := range []string{KindSite, KindApp} {
		recorded := bed.store.queuedOf(kind)
		if len(recorded) != 1 || recorded[0].Status != StatusFailed || recorded[0].FailureReason != orgguard.Message {
			t.Errorf("%s: the refused push is recorded as %+v, want one FAILED build saying why", kind, recorded)
		}
	}
}

func TestRebuild_RefusedWhileTheOrgOwnerIsSuspended(t *testing.T) {
	bed := newTestBed(true)
	bed.connectSite()
	bed.connectApp(AppSettings{})
	suspended := true
	bed.suspendOrg(&suspended)

	if _, err := bed.service.Rebuild(context.Background(), testProject, testSite, testUser); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("Rebuild err = %v, want ErrOrgSuspended", err)
	}
	if _, err := bed.service.RebuildApp(context.Background(), testProject, testApp, testUser); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("RebuildApp err = %v, want ErrOrgSuspended", err)
	}
	bed.nothingStarted(t)
	if len(bed.store.builds) != 0 {
		t.Errorf("%d builds recorded, want none", len(bed.store.builds))
	}
}

// A build queued before the suspension is stopped before its Job exists.
func TestDispatcher_DoesNotStartABuildQueuedBeforeTheSuspension(t *testing.T) {
	for _, kind := range []string{KindSite, KindApp} {
		bed := newTestBed(true)
		queued := bed.queue(t, kind)
		suspended := true
		bed.suspendOrg(&suspended)

		bed.nothingStarted(t)
		done := bed.store.build(queued.ID)
		if done.Status != StatusFailed || done.FailureReason != orgguard.Message {
			t.Errorf("%s: build %+v, want FAILED with the suspension message", kind, done)
		}
	}
}

func TestBuilds_AnUnsuspendedOrgAndAnUnreadableSuspensionList(t *testing.T) {
	bed := newTestBed(true)
	bed.connectSite()
	notSuspended := false
	bed.suspendOrg(&notSuspended)
	if _, err := bed.service.Rebuild(context.Background(), testProject, testSite, testUser); err != nil {
		t.Fatalf("an unsuspended org: %v", err)
	}
	bed.runAll()
	if len(bed.sites.started()) != 1 {
		t.Fatalf("the unsuspended build did not run")
	}

	unreadable := newTestBed(true)
	unreadable.connectSite()
	instances := fakestore.NewInstances()
	instances.Items[testProject] = &domain.DatabaseInstance{ProjectID: testProject, OrgID: "org-1"}
	unreadable.service.options.OrgGuard = orgguard.New(instances, func(context.Context) (map[string]bool, error) { return nil, errors.New("db down") })
	if _, err := unreadable.service.Rebuild(context.Background(), testProject, testSite, testUser); !errors.Is(err, orgguard.ErrCheckUnavailable) {
		t.Fatalf("err = %v, want ErrCheckUnavailable", err)
	}
	unreadable.nothingStarted(t)
}
