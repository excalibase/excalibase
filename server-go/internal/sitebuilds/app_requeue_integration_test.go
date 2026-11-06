//go:build integration

package sitebuilds_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitebuildstest"
)

var errNoRoom = fmt.Errorf("%w: spool (1 bytes free, 0 reserved)", sitebuilds.ErrTryLater)

func buildRow(t *testing.T, h *sitebuildstest.Harness, id string) (status, failure, wait string, requeues int) {
	t.Helper()
	err := h.SQL().QueryRow(`SELECT status, COALESCE(failure_reason, ''), COALESCE(wait_reason, ''), requeues FROM site_builds WHERE id = $1`, id).
		Scan(&status, &failure, &wait, &requeues)
	if err != nil {
		t.Fatalf("read the build: %v", err)
	}
	return status, failure, wait, requeues
}

func runTicks(h *sitebuildstest.Harness, ticks int) {
	for range ticks {
		// The retry delay is a millisecond here; a tick must see it pass.
		time.Sleep(5 * time.Millisecond)
		h.Dispatcher.Tick(context.Background())
		h.Dispatcher.Wait()
	}
}

// The real queue over a spool that stays full: three builds, no further attempt charged, then a clear failure.
func TestRequeue_ASpoolThatStaysFullEndsInFailureOnRealPostgres(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{AppsOn: true, Slots: 1})
	h.AppPublisher.SetRefuse(func(sitebuilds.AppPublish) error { return errNoRoom })
	app := h.AddApp(h.Project)
	h.ConnectApp(app, sitebuilds.AppSettings{Branch: "main", Builder: sitebuilds.AppBuilderBuildpacks})
	queued, err := h.Service.RebuildApp(context.Background(), h.Project, app, h.User)
	if err != nil {
		t.Fatal(err)
	}
	// Connecting queued a build too, which the rebuild superseded; only what the ticks charge is the requeues' cost.
	attempts := `SELECT COUNT(*) FROM app_build_attempts WHERE project_id = $1`
	charged := count(t, h, attempts, h.Project)
	runTicks(h, 30)
	status, failure, _, requeues := buildRow(t, h, queued.ID)
	if status != sitebuilds.StatusFailed || !strings.Contains(failure, "no room") || requeues != sitebuilds.MaxSpoolRequeues {
		t.Errorf("status %s, failure %q, requeues %d", status, failure, requeues)
	}
	if starts := len(h.AppRunner.Runs()); starts != sitebuilds.MaxSpoolRequeues+1 {
		t.Errorf("started %d builds", starts)
	}
	if after := count(t, h, attempts, h.Project); after != charged {
		t.Errorf("the rebuilds charged %d more attempts", after-charged)
	}
}

// The spool's trouble with one app leaves the install's only slot to the next app, through the real dispatcher.
func TestRequeue_AStuckBuildDoesNotHoldTheQueueThroughTheDispatcher(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{AppsOn: true, Slots: 1})
	stuck, other := h.AddApp(h.Project), h.AddApp(h.Project)
	settings := sitebuilds.AppSettings{Branch: "main", Builder: sitebuilds.AppBuilderBuildpacks}
	h.ConnectApp(stuck, settings)
	h.ConnectApp(other, settings)
	h.AppPublisher.SetRefuse(func(publish sitebuilds.AppPublish) error {
		if publish.AppID == stuck {
			return errNoRoom
		}
		return nil
	})
	stuckBuild, err := h.Service.RebuildApp(context.Background(), h.Project, stuck, h.User)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	otherBuild, err := h.Service.RebuildApp(context.Background(), h.Project, other, h.User)
	if err != nil {
		t.Fatal(err)
	}
	runTicks(h, 2)
	if status, _, _, _ := buildRow(t, h, otherBuild.ID); status != sitebuilds.StatusSucceeded {
		t.Errorf("after the stuck build's first refusal the other app is %s", status)
	}
	if status, _, _, requeues := buildRow(t, h, stuckBuild.ID); status == sitebuilds.StatusRunning || requeues == 0 {
		t.Errorf("the stuck build is %s after %d requeues", status, requeues)
	}
}

// One slot, 50 cycles: a build sent back waits behind one queued before it was sent back.
func TestRequeue_ABuildSentBackGoesBehindOtherProjects(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{AppsOn: true})
	ctx := context.Background()
	stuck := h.AddApp(h.Project)
	otherProject := "proj-" + uuid.NewString()[:8]
	other := h.AddApp(otherProject)
	queueAppBuild(t, h, h.Project, stuck, "", sitebuildstest.FreeAppLimits.Limits)
	time.Sleep(5 * time.Millisecond)
	queueAppBuild(t, h, otherProject, other, "", sitebuildstest.FreeAppLimits.Limits)
	claims := map[string]int{}
	for range 50 {
		build, err := h.Store.ClaimNext(ctx, 1, "r1", time.Now().Add(time.Minute), allKinds)
		if err != nil || build == nil {
			t.Fatalf("claim %v %v", build, err)
		}
		claims[build.AppID]++
		if err := h.Store.RetryBuildLater(ctx, build.ID, "r1", 0, "waiting"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if claims[stuck] != 25 || claims[other] != 25 {
		t.Errorf("stuck claimed %d times, other %d", claims[stuck], claims[other])
	}
}

func TestRequeue_ABuildWaitsOutItsDelayAndThenRunsAgain(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{AppsOn: true})
	ctx := context.Background()
	app, other := h.AddApp(h.Project), h.AddApp(h.Project)
	first := queueAppBuild(t, h, h.Project, app, "", sitebuildstest.FreeAppLimits.Limits)
	got := claim(t, h, 1)
	if got == nil || got.ID != first.ID {
		t.Fatalf("claimed %+v", got)
	}
	if err := h.Store.RetryBuildLater(ctx, got.ID, "replica-"+h.Project, time.Hour, "no room yet"); err != nil {
		t.Fatal(err)
	}
	if early := claim(t, h, 1); early != nil {
		t.Fatalf("claimed during its delay: %+v", early)
	}
	later := queueAppBuild(t, h, h.Project, other, "", sitebuildstest.FreeAppLimits.Limits)
	if next := claim(t, h, 1); next == nil || next.ID != later.ID {
		t.Fatalf("the slot was not free for another app: %+v", next)
	}
	finish(t, h, later)
	target := sitebuilds.Target{ProjectID: h.Project, Kind: sitebuilds.KindApp, ID: app}
	shown, err := h.Store.GetBuild(ctx, target, first.ID)
	if err != nil || shown.WaitReason != "no room yet" || shown.Requeues != 1 {
		t.Fatalf("shown %+v, %v", shown, err)
	}
	listed, err := h.Store.ListBuilds(ctx, target, 5)
	if err != nil || len(listed) != 1 || listed[0].WaitReason != "no room yet" {
		t.Fatalf("listed %+v, %v", listed, err)
	}
	if _, err := h.SQL().Exec(`UPDATE site_builds SET retry_at = NOW() - INTERVAL '1 second' WHERE id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	again := claim(t, h, 1)
	if again == nil || again.ID != first.ID || again.Requeues != 1 || again.WaitReason != "" {
		t.Fatalf("after the delay: %+v", again)
	}
}

// A shutdown's hand back costs no retry and no place in the queue.
func TestRequeue_AShutdownHandBackKeepsItsPlaceAndCount(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{AppsOn: true})
	ctx := context.Background()
	app, other := h.AddApp(h.Project), h.AddApp(h.Project)
	first := queueAppBuild(t, h, h.Project, app, "", sitebuildstest.FreeAppLimits.Limits)
	time.Sleep(5 * time.Millisecond)
	queueAppBuild(t, h, h.Project, other, "", sitebuildstest.FreeAppLimits.Limits)
	got := claim(t, h, 1)
	if err := h.Store.RequeueBuild(ctx, got.ID, "replica-"+h.Project); err != nil {
		t.Fatal(err)
	}
	again := claim(t, h, 1)
	if again == nil || again.ID != first.ID || again.Requeues != 0 {
		t.Fatalf("after the hand back: %+v", again)
	}
}

func TestRequeue_TheWrongHolderChangesNothing(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{AppsOn: true})
	ctx := context.Background()
	app := h.AddApp(h.Project)
	queued := queueAppBuild(t, h, h.Project, app, "", sitebuildstest.FreeAppLimits.Limits)
	got, err := h.Store.ClaimNext(ctx, 100, "A", time.Now().Add(time.Minute), []string{sitebuilds.KindApp})
	if err != nil || got == nil || got.ID != queued.ID {
		t.Fatalf("claim %+v %v", got, err)
	}
	if err := h.Store.RetryBuildLater(ctx, got.ID, "B", time.Minute, "no"); !errors.Is(err, sitebuilds.ErrLeaseLost) {
		t.Errorf("the wrong holder got %v", err)
	}
	if err := h.Store.RequeueBuild(ctx, got.ID, "B"); err != nil {
		t.Error(err)
	}
	if status, _, wait, requeues := buildRow(t, h, got.ID); status != sitebuilds.StatusRunning || wait != "" || requeues != 0 {
		t.Errorf("after B: %s %q %d", status, wait, requeues)
	}
	if next, err := h.Store.ClaimNext(ctx, 100, "B", time.Now().Add(time.Minute), []string{sitebuilds.KindApp}); err != nil || next != nil {
		t.Errorf("B claimed the same app during A's run: %+v %v", next, err)
	}
}

// A newer push wins whichever way round the two meet, and starts its own count.
func TestRequeue_ANewerBuildStillCancelsTheOlder(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{AppsOn: true})
	ctx := context.Background()
	app := h.AddApp(h.Project)
	queueAppBuild(t, h, h.Project, app, "", sitebuildstest.FreeAppLimits.Limits)
	first := claim(t, h, 100)
	newer := queueAppBuild(t, h, h.Project, app, "", sitebuildstest.FreeAppLimits.Limits)
	if err := h.Store.RetryBuildLater(ctx, first.ID, "replica-"+h.Project, 0, "no room"); err != nil {
		t.Fatal(err)
	}
	status, failure, _, _ := buildRow(t, h, first.ID)
	if status != sitebuilds.StatusCancelled || !strings.Contains(failure, "superseded") {
		t.Errorf("the older, a newer waiting: %s %q", status, failure)
	}
	second := claim(t, h, 100)
	if second == nil || second.ID != newer.ID {
		t.Fatalf("claimed %+v", second)
	}
	if err := h.Store.RetryBuildLater(ctx, second.ID, "replica-"+h.Project, time.Hour, "no room"); err != nil {
		t.Fatal(err)
	}
	newest := queueAppBuild(t, h, h.Project, app, "", sitebuildstest.FreeAppLimits.Limits)
	status, _, _, _ = buildRow(t, h, second.ID)
	if status != sitebuilds.StatusCancelled {
		t.Errorf("a build waiting out its delay, then a push: %s", status)
	}
	third := claim(t, h, 100)
	if third == nil || third.ID != newest.ID || third.Requeues != 0 {
		t.Errorf("the newest: %+v", third)
	}
}

// A cancelled build shows why it was cancelled, not the wait it no longer does.
func TestRequeue_AWaitReasonIsShownOnlyWhileQueued(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{AppsOn: true})
	ctx := context.Background()
	app := h.AddApp(h.Project)
	queueAppBuild(t, h, h.Project, app, "", sitebuildstest.FreeAppLimits.Limits)
	first := claim(t, h, 100)
	if err := h.Store.RetryBuildLater(ctx, first.ID, "replica-"+h.Project, time.Hour, "no room"); err != nil {
		t.Fatal(err)
	}
	queueAppBuild(t, h, h.Project, app, "", sitebuildstest.FreeAppLimits.Limits)
	shown, err := h.Store.GetBuild(ctx, sitebuilds.Target{ProjectID: h.Project, Kind: sitebuilds.KindApp, ID: app}, first.ID)
	if err != nil || shown.Status != sitebuilds.StatusCancelled || shown.WaitReason != "" {
		t.Errorf("shown %+v, %v", shown, err)
	}
}

// An app build sent back still gives the last slot to a site build waiting beside it.
func TestRequeue_AStuckAppLeavesTheLastSlotToASite(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{AppsOn: true})
	app := h.AddApp(h.Project)
	queueAppBuild(t, h, h.Project, app, "", sitebuildstest.FreeAppLimits.Limits)
	first := claim(t, h, 1)
	if err := h.Store.RetryBuildLater(context.Background(), first.ID, "replica-"+h.Project, 0, "no room"); err != nil {
		t.Fatal(err)
	}
	h.Connect()
	if next := claim(t, h, 1); next == nil || next.Kind != sitebuilds.KindSite {
		t.Errorf("the slot went to %+v", next)
	}
}
