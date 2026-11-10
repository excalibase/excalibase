package sitebuilds

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

var errNoRoom = fmt.Errorf("%w: the spool (12345 bytes free, 678 reserved)", ErrTryLater)

// lockedBuffer collects the log, which a build's goroutines write while the test reads.
type lockedBuffer struct {
	mu  sync.Mutex
	out bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.out.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.out.String()
}

func captureLog(t *testing.T) *lockedBuffer {
	t.Helper()
	captured, previous := &lockedBuffer{}, log.Writer()
	log.SetOutput(captured)
	t.Cleanup(func() { log.SetOutput(previous) })
	return captured
}

// One push costs a small fixed number of builds and one attempt of the hourly limit, however long the spool stays full.
func TestAppBuild_ASpoolThatStaysFullFailsAfterABoundedNumberOfBuilds(t *testing.T) {
	bed := newTestBed(true)
	bed.publisher.err = errNoRoom
	queued := queueApp(t, bed, AppSettings{Builder: AppBuilderBuildpacks})
	for range 200 {
		bed.runAll()
	}
	done := bed.store.build(queued.ID)
	if starts := len(bed.apps.started()); starts != MaxSpoolRequeues+1 {
		t.Errorf("started %d builds, want %d", starts, MaxSpoolRequeues+1)
	}
	if done.Status != StatusFailed || done.FailureKind != FailPlatform || !strings.Contains(done.FailureReason, "no room") {
		t.Errorf("after the bound: %+v", done)
	}
	if strings.Contains(done.FailureReason, "12345") {
		t.Errorf("the customer saw the spool's numbers: %q", done.FailureReason)
	}
	if attempts := bed.store.appAttempts[testProject]; attempts != 1 {
		t.Errorf("charged %d attempts for one push", attempts)
	}
	if minted, revoked := bed.provider.minted, len(bed.provider.revokedTokens()); minted != MaxSpoolRequeues+1 || revoked != minted {
		t.Errorf("minted %d clone tokens, revoked %d", minted, revoked)
	}
}

func TestAppBuild_AWaitingBuildSaysWhy(t *testing.T) {
	bed := newTestBed(true)
	bed.publisher.err = errNoRoom
	queued := queueApp(t, bed, AppSettings{Builder: AppBuilderBuildpacks})
	bed.runAll()
	waiting := bed.store.build(queued.ID)
	if waiting.Status != StatusQueued || waiting.Requeues != 1 || waiting.FailureReason != "" || waiting.FinishedAt != nil {
		t.Fatalf("a full spool left the build %+v", waiting)
	}
	if !strings.Contains(waiting.WaitReason, "no room") || !strings.Contains(waiting.WaitReason, fmt.Sprintf("retry 1 of %d", MaxSpoolRequeues)) {
		t.Errorf("wait reason %q", waiting.WaitReason)
	}
	if strings.Contains(waiting.WaitReason, "12345") {
		t.Errorf("the customer saw the spool's numbers: %q", waiting.WaitReason)
	}
}

func TestAppBuild_EveryRefusalIsLoggedWithItsNumbers(t *testing.T) {
	captured := captureLog(t)
	bed := newTestBed(true)
	bed.publisher.refuse = func(call int) error {
		if call <= 3 {
			return errNoRoom
		}
		return nil
	}
	bed.dispatcher.options.PublishWait = time.Minute
	queued := queueApp(t, bed, AppSettings{Builder: AppBuilderBuildpacks})
	bed.runAll()
	if refusals := strings.Count(captured.String(), "12345 bytes free, 678 reserved"); refusals != 3 {
		t.Errorf("logged %d of 3 refusals:\n%s", refusals, captured.String())
	}
	if !strings.Contains(captured.String(), queued.ID) {
		t.Errorf("the log does not name the build:\n%s", captured.String())
	}
}

// Another publish in flight clears soon: the finished image is kept and no rebuild runs.
func TestAppBuild_ATransientRefusalWaitsInPlaceAndKeepsTheImage(t *testing.T) {
	bed := newTestBed(true)
	bed.dispatcher.options.PublishWait = time.Minute
	var removedWhileWaiting int
	bed.publisher.refuse = func(call int) error {
		removedWhileWaiting += len(bed.apps.removedIDs())
		if call <= 3 {
			return errNoRoom
		}
		return nil
	}
	queued := queueApp(t, bed, AppSettings{Builder: AppBuilderBuildpacks})
	bed.runAll()
	done := bed.store.build(queued.ID)
	if done.Status != StatusSucceeded || done.Requeues != 0 || done.DeployID == "" {
		t.Errorf("after the wait: %+v", done)
	}
	if starts, calls := len(bed.apps.started()), bed.publisher.calls(); starts != 1 || calls != 4 {
		t.Errorf("started %d builds and published %d times, want 1 and 4", starts, calls)
	}
	if removedWhileWaiting != 0 {
		t.Error("the Job holding the image was removed during the wait")
	}
	if !strings.Contains(bed.store.logs[queued.ID], "waiting for room") {
		t.Errorf("the log does not say the build waits: %q", bed.store.logs[queued.ID])
	}
}

// The wait outlasts the lease, so a healthy replica must keep renewing it or another would fail the build as lost.
func TestAppBuild_TheLeaseIsRenewedThroughTheWait(t *testing.T) {
	bed := newTestBed(true)
	bed.dispatcher.options.Lease = 30 * time.Millisecond
	bed.dispatcher.options.PublishWait = time.Minute
	var atFirstRefusal int
	waited := time.Time{}
	bed.publisher.refuse = func(call int) error {
		if call == 1 {
			atFirstRefusal, waited = bed.store.renewals(), time.Now()
		}
		if time.Since(waited) < 100*time.Millisecond {
			return errNoRoom
		}
		return nil
	}
	queued := queueApp(t, bed, AppSettings{Builder: AppBuilderBuildpacks})
	bed.runAll()
	if renewed := bed.store.renewals() - atFirstRefusal; renewed < 2 {
		t.Errorf("renewed the lease %d times through a wait of three leases", renewed)
	}
	if done := bed.store.build(queued.ID); done.Status != StatusSucceeded {
		t.Errorf("after the wait: %+v", done)
	}
}

// Before the wait the source was checked once; a repository disconnected during it must not get its image deployed.
func TestAppBuild_DisconnectingDuringTheWaitStopsIt(t *testing.T) {
	bed := newTestBed(true)
	bed.dispatcher.options.PublishWait = time.Minute
	bed.publisher.refuse = func(call int) error {
		if call == 1 {
			if err := bed.store.DeleteAppConnection(context.Background(), testProject, testApp); err != nil {
				t.Error(err)
			}
			return errNoRoom
		}
		return nil
	}
	queued := queueApp(t, bed, AppSettings{Builder: AppBuilderBuildpacks})
	bed.runAll()
	done := bed.store.build(queued.ID)
	if done.Status != StatusFailed || !strings.Contains(done.FailureReason, "disconnected") || done.DeployID != "" {
		t.Errorf("after the disconnect: %+v", done)
	}
	if calls := bed.publisher.calls(); calls != 1 {
		t.Errorf("published %d times after the disconnect", calls)
	}
}

// A stopping replica is not the spool's fault: the build goes back uncounted, with no wait reason.
func TestAppBuild_ShutdownDuringTheWaitHandsBackWithoutCounting(t *testing.T) {
	bed := newTestBed(true)
	bed.dispatcher.options.PublishWait = time.Minute
	bed.publisher.refuse = func(call int) error {
		if call == 1 {
			bed.dispatcher.interruptOnce.Do(func() { close(bed.dispatcher.interrupted) })
		}
		return errNoRoom
	}
	queued := queueApp(t, bed, AppSettings{Builder: AppBuilderBuildpacks})
	bed.runAll()
	waiting := bed.store.build(queued.ID)
	if waiting.Status != StatusQueued || waiting.Requeues != 0 || waiting.WaitReason != "" {
		t.Errorf("after the shutdown: %+v", waiting)
	}
}

// A spool too small for the plan's image can never publish, so nothing is minted, run or charged twice.
func TestAppBuild_ASpoolThatCanNeverFitRefusesBeforeBuilding(t *testing.T) {
	bed := newTestBed(true)
	bed.publisher.capacityErr = &BuildFailure{Kind: FailPlatform, Err: errors.New("the spool is too small for this plan")}
	queued := queueApp(t, bed, AppSettings{Builder: AppBuilderBuildpacks})
	bed.runAll()
	done := bed.store.build(queued.ID)
	if done.Status != StatusFailed || done.FailureKind != FailPlatform || !strings.Contains(done.FailureReason, "too small") {
		t.Errorf("outcome %+v", done)
	}
	if starts, minted, published := len(bed.apps.started()), bed.provider.minted, bed.publisher.calls(); starts != 0 || minted != 0 || published != 0 {
		t.Errorf("started %d, minted %d, published %d", starts, minted, published)
	}
}

func TestAppBuild_ACapacityThatCannotBeReadRefusesWithoutDetail(t *testing.T) {
	bed := newTestBed(true)
	bed.publisher.capacityErr = errors.New("statfs /secret/path: gone")
	queued := queueApp(t, bed, AppSettings{Builder: AppBuilderBuildpacks})
	bed.runAll()
	done := bed.store.build(queued.ID)
	if done.Status != StatusFailed || strings.Contains(done.FailureReason, "/secret/path") || len(bed.apps.started()) != 0 {
		t.Errorf("outcome %+v", done)
	}
}

// Only an app's publish can answer try later; a site's failed collect stays that build's outcome.
func TestSiteBuild_NeverRequeuesOnAFailedDeploy(t *testing.T) {
	bed := newTestBed(true)
	bed.dispatcher.options.SpoolDir = t.TempDir() + "/missing"
	queued := queueSite(t, bed)
	bed.runAll()
	done := bed.store.build(queued.ID)
	if done.Status != StatusFailed || done.Requeues != 0 || done.WaitReason != "" {
		t.Errorf("outcome %+v", done)
	}
	if starts := len(bed.sites.started()); starts != 1 {
		t.Errorf("started %d site builds", starts)
	}
}

// The Job's time limit does not stop for the wait. Once it ends there is no image left to wait with, so the build
// goes back for its retry; a publish now would fail for want of a pod, ending a healthy build for good.
func TestAppBuild_AJobThatEndsDuringTheWaitGoesBackInsteadOfFailing(t *testing.T) {
	bed := newTestBed(true)
	bed.dispatcher.options.PublishWait = time.Minute
	bed.publisher.refuse = func(call int) error {
		if call > 1 {
			return errors.New("the build has no pod to collect from")
		}
		bed.apps.mu.Lock()
		defer bed.apps.mu.Unlock()
		bed.apps.current = RunStatus{Phase: RunFailed, Failure: FailPlatform, Detail: "past its deadline"}
		return errNoRoom
	}
	queued := queueApp(t, bed, AppSettings{Builder: AppBuilderBuildpacks})
	bed.runAll()
	waiting := bed.store.build(queued.ID)
	if waiting.Status != StatusQueued || waiting.Requeues != 1 || waiting.FailureReason != "" {
		t.Errorf("after the Job ended: %+v", waiting)
	}
	if calls := bed.publisher.calls(); calls != 1 {
		t.Errorf("published %d times after the Job ended", calls)
	}
}

// A status read that fails says nothing about the Job, so it costs no image.
func TestAppBuild_AStatusReadThatFailsKeepsWaiting(t *testing.T) {
	bed := newTestBed(true)
	bed.dispatcher.options.PublishWait = time.Minute
	bed.publisher.refuse = func(call int) error {
		bed.apps.mu.Lock()
		defer bed.apps.mu.Unlock()
		switch call {
		case 1:
			bed.apps.statusErr = errors.New("the API server blinked")
			return errNoRoom
		case 2:
			bed.apps.statusErr = nil
			return errNoRoom
		}
		return nil
	}
	queued := queueApp(t, bed, AppSettings{Builder: AppBuilderBuildpacks})
	bed.runAll()
	if done := bed.store.build(queued.ID); done.Status != StatusSucceeded || done.Requeues != 0 {
		t.Errorf("after the failed read: %+v", done)
	}
	if calls := bed.publisher.calls(); calls != 3 {
		t.Errorf("published %d times, want 3", calls)
	}
}

// The log must not promise an image the Job's deadline can still take away.
func TestAppBuild_TheWaitNoteMakesNoPromiseAboutTheImage(t *testing.T) {
	bed := newTestBed(true)
	bed.publisher.err = errNoRoom
	queued := queueApp(t, bed, AppSettings{Builder: AppBuilderBuildpacks})
	bed.runAll()
	note := bed.store.logs[queued.ID]
	if !strings.Contains(note, "waiting for room") || strings.Contains(note, "is kept") {
		t.Errorf("the note: %q", note)
	}
	if strings.Contains(note, "== retry ==") {
		t.Errorf("the first run calls itself a retry: %q", note)
	}
}

func TestAppBuild_EachRetryWaitsLongerAndSaysWhichItIs(t *testing.T) {
	bed := newTestBed(true)
	bed.dispatcher.options.RequeueDelay = 7 * time.Millisecond
	bed.publisher.err = errNoRoom
	queued := queueApp(t, bed, AppSettings{Builder: AppBuilderBuildpacks})
	for range 10 {
		bed.runAll()
	}
	want := []time.Duration{7 * time.Millisecond, 14 * time.Millisecond}
	if !slices.Equal(bed.store.retryDelays, want) {
		t.Errorf("delays %v, want %v", bed.store.retryDelays, want)
	}
	done := bed.store.build(queued.ID)
	if !strings.Contains(done.Log, "== retry ==") || !strings.Contains(done.Log, fmt.Sprintf("retry %d of %d", MaxSpoolRequeues, MaxSpoolRequeues)) {
		t.Errorf("the last run's log: %q", done.Log)
	}
}

func TestDispatcherOptions_TheWaitLeavesThePublishHalfTheBudget(t *testing.T) {
	cases := []struct {
		name               string
		wait, budget, want time.Duration
	}{
		{"the defaults", 0, 0, 10 * time.Minute},
		{"a short budget caps the default wait", 0, 8 * time.Minute, 4 * time.Minute},
		{"a long wait is capped", time.Hour, 20 * time.Minute, 10 * time.Minute},
		{"a short wait stands", time.Minute, 20 * time.Minute, time.Minute},
	}
	for _, tc := range cases {
		got := DispatcherOptions{PublishWait: tc.wait, DeployBudget: tc.budget}.withDefaults()
		if got.PublishWait != tc.want {
			t.Errorf("%s: wait %s, want %s", tc.name, got.PublishWait, tc.want)
		}
	}
	if delay := (DispatcherOptions{}).withDefaults().RequeueDelay; delay != 5*time.Minute {
		t.Errorf("default delay %s", delay)
	}
}
