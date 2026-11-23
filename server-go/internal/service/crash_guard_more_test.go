package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

type leaderStub struct {
	leading bool
	err     error
	checks  chan struct{}
}

func (l leaderStub) IsLeader(context.Context) (bool, error) {
	select {
	case l.checks <- struct{}{}:
	default:
	}
	return l.leading, l.err
}

type crashFailingInstances struct{}

func (crashFailingInstances) FindAll() ([]*domain.DatabaseInstance, error) {
	return nil, errors.New("db down")
}

type crashFailingApps struct{}

func (crashFailingApps) ListSuspendable() ([]*apphost.App, error) { return nil, errors.New("db down") }

// Only the leading replica sweeps; the loop keeps running through a failed check.
func TestCrashGuard_StartSweepsOnlyWhileLeading(t *testing.T) {
	rig := newGuardRig(t)
	rig.pods.apps[guardNS+"/"+guardApp] = appStateAt(0, "", 0)
	leader := leaderStub{leading: true, checks: make(chan struct{}, 1)}
	stop := rig.guard.Start(context.Background(), leader, time.Millisecond)
	defer stop()
	deadline := time.Now().Add(30 * time.Second)
	for {
		rig.pods.mu.Lock()
		asked := len(rig.pods.asked)
		rig.pods.mu.Unlock()
		if asked > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a leading replica never swept")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCrashGuard_AFollowerOrAFailedLeaderCheckDoesNotSweep(t *testing.T) {
	for _, leader := range []leaderStub{{leading: false}, {err: errors.New("lock lost")}} {
		rig := newGuardRig(t)
		rig.guard.sweepIfLeader(context.Background(), leader)
		if len(rig.pods.asked) != 0 {
			t.Errorf("%+v swept", leader)
		}
	}
}

func TestCrashGuard_ReadFailuresAreSurvived(t *testing.T) {
	rig := newGuardRig(t)
	rig.guard.config.Instances = crashFailingInstances{}
	rig.guard.Sweep(context.Background())

	rig = newGuardRig(t)
	rig.guard.config.Apps = crashFailingApps{}
	rig.guard.Sweep(context.Background())

	rig = newGuardRig(t)
	rig.pods.err = errors.New("api down")
	rig.guard.Sweep(context.Background())
	if len(rig.stopper.calls) != 0 {
		t.Fatal("a failed read must never stop anything")
	}
}

// The database alert is built and raised through the real alert service, and resolves when the loop stops.
func TestCrashGuard_DatabaseAlertRaisesAndResolves(t *testing.T) {
	rig := newGuardRig(t)
	alerts := NewAlertingService(t.TempDir())
	rig.guard.config.Alerts = alerts
	rig.guard.config.Apps = &crashApps{}
	db := func(restarts int32, reason string) []k8s.ContainerCrash {
		return []k8s.ContainerCrash{{PodUID: "uid-pg", Container: "postgres", RestartCount: restarts, LastExit: &k8s.ContainerExit{Reason: reason, ExitCode: 1}}}
	}
	rig.pods.databases[guardNS] = db(0, "Error")
	rig.guard.Sweep(context.Background())
	rig.now = rig.now.Add(time.Minute)
	rig.pods.databases[guardNS] = db(10, "Error")
	rig.guard.Sweep(context.Background())
	active := alerts.GetActiveAlertsForProject(guardProject)
	if len(active) != 1 || active[0].Metric != databaseCrashMetric || active[0].Severity != "CRITICAL" {
		t.Fatalf("active alerts = %+v", active)
	}
	rig.now = rig.now.Add(2 * time.Hour)
	rig.guard.Sweep(context.Background())
	if left := alerts.GetActiveAlertsForProject(guardProject); len(left) != 0 {
		t.Fatalf("the alert stayed raised after the crashes stopped: %+v", left)
	}
}

func TestCrashGuard_ADatabaseOOMKillDoesNotAlert(t *testing.T) {
	rig := newGuardRig(t)
	rig.guard.config.Apps = &crashApps{}
	rig.pods.databases[guardNS] = []k8s.ContainerCrash{{PodUID: "u", Container: "postgres", RestartCount: 0}}
	rig.guard.Sweep(context.Background())
	rig.pods.databases[guardNS] = []k8s.ContainerCrash{{PodUID: "u", Container: "postgres", RestartCount: 50, LastExit: &k8s.ContainerExit{Reason: "OOMKilled", ExitCode: 137}}}
	rig.guard.Sweep(context.Background())
	for _, call := range rig.alerts.calls {
		if call.breached {
			t.Fatalf("an OOM kill raised %+v", call)
		}
	}
}

func TestCrashGuard_AStopThatLeftNoMarkerStillCountsAsStopped(t *testing.T) {
	rig := newGuardRig(t)
	rig.stopper.err = nil
	rig.guard.config.Stopper = markerFailure{}
	rig.sweepAt(0, 0, "", 0)
	rig.sweepAt(1, 10, "Error", 1)
	if len(rig.mail.stopped) != 1 {
		t.Fatal("the app was stopped, so its owner must still be told")
	}
}

type markerFailure struct{}

func (markerFailure) SuspendForCrashLoop(context.Context, string, string, int, time.Duration) (*apphost.Suspension, error) {
	return &apphost.Suspension{Kind: apphost.SuspensionCrashLoop, Restarts: 10}, errors.New("annotate failed")
}

func TestCrashGuard_DefaultsItsClock(t *testing.T) {
	if NewCrashGuard(CrashGuardConfig{}).config.Now == nil {
		t.Fatal("no clock")
	}
}

func TestWindowWords(t *testing.T) {
	if windowWords(time.Hour) != "hour" || windowWords(90*time.Minute) != "1h30m0s" {
		t.Error("window wording")
	}
}

// After a stop the app's count starts again: Start again, then one more crash, must not stop it a second time.
func TestCrashGuard_AnAppStartedAgainIsCountedFromZero(t *testing.T) {
	rig := newGuardRig(t)
	rig.sweepAt(0, 0, "", 0)
	rig.sweepAt(1, 10, "Error", 1)
	if len(rig.stopper.calls) != 1 {
		t.Fatalf("calls = %d, want the first stop", len(rig.stopper.calls))
	}
	rig.sweepAt(1, 0, "", 0) // a new pod after Start again: baseline
	rig.sweepAt(1, 1, "Error", 1)
	if len(rig.stopper.calls) != 1 {
		t.Fatalf("one crash after Start again stopped the app again: %+v", rig.stopper.calls)
	}
}
