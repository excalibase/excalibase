package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

type crashPods struct {
	mu        sync.Mutex
	apps      map[string][]k8s.ContainerCrash // keyed "namespace/appID"
	databases map[string][]k8s.ContainerCrash // keyed namespace
	asked     []string
	err       error
}

func (p *crashPods) AppContainerStates(_ context.Context, namespace, appID string) ([]k8s.ContainerCrash, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, namespace)
	return p.apps[namespace+"/"+appID], p.err
}

func (p *crashPods) DatabaseContainerStates(_ context.Context, namespace string) ([]k8s.ContainerCrash, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, namespace)
	return p.databases[namespace], p.err
}

type crashApps struct{ apps []*apphost.App }

func (a *crashApps) ListSuspendable() ([]*apphost.App, error) { return a.apps, nil }

type stopCall struct {
	projectID, appID string
	crashes          int
}

type crashStopper struct {
	calls []stopCall
	err   error
}

func (s *crashStopper) SuspendForCrashLoop(_ context.Context, projectID, appID string, crashes int, _ time.Duration) (*apphost.Suspension, error) {
	s.calls = append(s.calls, stopCall{projectID, appID, crashes})
	if s.err != nil {
		return nil, s.err
	}
	return &apphost.Suspension{Kind: apphost.SuspensionCrashLoop, Restarts: crashes}, nil
}

type crashInstances struct{ items []*domain.DatabaseInstance }

func (i crashInstances) FindAll() ([]*domain.DatabaseInstance, error) { return i.items, nil }

type trackCall struct {
	projectID, metric string
	breached          bool
}

type crashAlerts struct{ calls []trackCall }

func (a *crashAlerts) Track(projectID, metric string, breached bool, _ func() domain.Alert) AlertChange {
	a.calls = append(a.calls, trackCall{projectID, metric, breached})
	return AlertChange{}
}

type crashMail struct{ stopped []string }

func (m *crashMail) AppStopped(_ context.Context, _ *domain.DatabaseInstance, app *apphost.App, _ *apphost.Suspension) {
	m.stopped = append(m.stopped, app.ID)
}

const (
	guardProject = "proj-aaaa"
	guardNS      = "ns-aaaa"
	guardApp     = "app-1"
)

type guardRig struct {
	guard   *CrashGuard
	pods    *crashPods
	stopper *crashStopper
	alerts  *crashAlerts
	mail    *crashMail
	now     time.Time
}

func newGuardRig(t *testing.T) *guardRig {
	t.Helper()
	rig := &guardRig{
		pods:    &crashPods{apps: map[string][]k8s.ContainerCrash{}, databases: map[string][]k8s.ContainerCrash{}},
		stopper: &crashStopper{}, alerts: &crashAlerts{}, mail: &crashMail{},
		now: time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC),
	}
	rig.guard = NewCrashGuard(CrashGuardConfig{
		Apps:      &crashApps{apps: []*apphost.App{{ID: guardApp, ProjectID: guardProject, Name: "web", Status: apphost.StatusRunning}}},
		Instances: crashInstances{items: []*domain.DatabaseInstance{{ProjectID: guardProject, Namespace: guardNS}}},
		Pods:      rig.pods, Stopper: rig.stopper, Alerts: rig.alerts, Notifier: rig.mail,
		Threshold: 10, ProtectedNamespaces: []string{"excalibase-platform"},
		Now: func() time.Time { return rig.now },
	})
	return rig
}

func appStateAt(restarts int32, reason string, exitCode int32) []k8s.ContainerCrash {
	return []k8s.ContainerCrash{{PodUID: "uid-1", PodName: "web-1", Container: "web", RestartCount: restarts,
		LastExit: &k8s.ContainerExit{Reason: reason, ExitCode: exitCode}}}
}

// sweepAt reports the app's restart count after minutes more have passed.
func (r *guardRig) sweepAt(minutes int, restarts int32, reason string, exitCode int32) {
	r.now = r.now.Add(time.Duration(minutes) * time.Minute)
	r.pods.apps[guardNS+"/"+guardApp] = appStateAt(restarts, reason, exitCode)
	r.guard.Sweep(context.Background())
}

func TestCrashGuard_NineCrashesDoNothingTenStopTheApp(t *testing.T) {
	rig := newGuardRig(t)
	rig.sweepAt(0, 0, "", 0) // the first sight is a baseline
	rig.sweepAt(1, 9, "Error", 1)
	if len(rig.stopper.calls) != 0 {
		t.Fatalf("9 crashes stopped the app: %+v", rig.stopper.calls)
	}
	rig.sweepAt(1, 10, "Error", 1)
	if len(rig.stopper.calls) != 1 || rig.stopper.calls[0] != (stopCall{guardProject, guardApp, 10}) {
		t.Fatalf("10 crashes must stop the app once with the count: %+v", rig.stopper.calls)
	}
	if len(rig.mail.stopped) != 1 || rig.mail.stopped[0] != guardApp {
		t.Errorf("the owner must be told: %v", rig.mail.stopped)
	}
}

func TestCrashGuard_AnOOMKillIsNotACrash(t *testing.T) {
	rig := newGuardRig(t)
	rig.sweepAt(0, 0, "", 0)
	rig.sweepAt(1, 25, "OOMKilled", 137)
	if len(rig.stopper.calls) != 0 {
		t.Fatalf("OOM kills stopped the app: %+v", rig.stopper.calls)
	}
}

func TestCrashGuard_AContainerThatExitedCleanlyIsNotACrash(t *testing.T) {
	rig := newGuardRig(t)
	rig.sweepAt(0, 0, "", 0)
	rig.sweepAt(1, 20, "Completed", 0)
	if len(rig.stopper.calls) != 0 {
		t.Fatalf("clean exits stopped the app: %+v", rig.stopper.calls)
	}
}

func TestCrashGuard_OnlyTheLastHourCounts(t *testing.T) {
	rig := newGuardRig(t)
	rig.sweepAt(0, 0, "", 0)
	rig.sweepAt(1, 6, "Error", 1)
	rig.sweepAt(61, 6, "Error", 1) // those six are now over an hour old
	rig.sweepAt(1, 10, "Error", 1) // four more: ten crashes overall, four in the hour
	if len(rig.stopper.calls) != 0 {
		t.Fatalf("crashes older than an hour were counted: %+v", rig.stopper.calls)
	}
}

func TestCrashGuard_RestartsAlreadyOnAPodWhenItIsFirstSeenAreNotCounted(t *testing.T) {
	rig := newGuardRig(t)
	rig.sweepAt(0, 40, "Error", 1)
	if len(rig.stopper.calls) != 0 {
		t.Fatalf("a baseline must not trip the guard: %+v", rig.stopper.calls)
	}
}

func TestCrashGuard_APodBeingDeletedIsARolloutNotACrash(t *testing.T) {
	rig := newGuardRig(t)
	rig.sweepAt(0, 0, "", 0)
	rig.now = rig.now.Add(time.Minute)
	states := appStateAt(30, "Error", 143)
	states[0].Deleting = true
	rig.pods.apps[guardNS+"/"+guardApp] = states
	rig.guard.Sweep(context.Background())
	if len(rig.stopper.calls) != 0 {
		t.Fatalf("a rollout's exits stopped the app: %+v", rig.stopper.calls)
	}
}

func TestCrashGuard_ADatabaseOnlyAlerts(t *testing.T) {
	rig := newGuardRig(t)
	rig.guard = NewCrashGuard(CrashGuardConfig{
		Apps:      &crashApps{},
		Instances: crashInstances{items: []*domain.DatabaseInstance{{ProjectID: guardProject, Namespace: guardNS}}},
		Pods:      rig.pods, Stopper: rig.stopper, Alerts: rig.alerts, Threshold: 10,
		Now: func() time.Time { return rig.now },
	})
	db := func(restarts int32) []k8s.ContainerCrash {
		return []k8s.ContainerCrash{{PodUID: "uid-pg", PodName: "pg-1", Container: "postgres", RestartCount: restarts,
			LastExit: &k8s.ContainerExit{Reason: "Error", ExitCode: 1}}}
	}
	rig.pods.databases[guardNS] = db(3344)
	rig.guard.Sweep(context.Background())
	rig.now = rig.now.Add(time.Minute)
	rig.pods.databases[guardNS] = db(3354)
	rig.guard.Sweep(context.Background())

	if len(rig.stopper.calls) != 0 {
		t.Fatalf("a database must never be stopped: %+v", rig.stopper.calls)
	}
	last := rig.alerts.calls[len(rig.alerts.calls)-1]
	if last != (trackCall{guardProject, "databaseCrashLoop", true}) {
		t.Fatalf("last alert call = %+v, want the database alert raised", last)
	}
	if first := rig.alerts.calls[0]; first.breached {
		t.Errorf("the baseline sweep raised %+v", first)
	}
}

func TestCrashGuard_ExemptProjectsAndPlatformNamespacesAreNeverRead(t *testing.T) {
	rig := newGuardRig(t)
	rig.guard = NewCrashGuard(CrashGuardConfig{
		Apps: &crashApps{apps: []*apphost.App{
			{ID: "app-x", ProjectID: "proj-jfp7kx46kb", Status: apphost.StatusRunning},
			{ID: "app-p", ProjectID: "proj-plat", Status: apphost.StatusRunning},
		}},
		Instances: crashInstances{items: []*domain.DatabaseInstance{
			{ProjectID: "proj-jfp7kx46kb", Namespace: "ns-exempt"},
			{ProjectID: "proj-plat", Namespace: "excalibase-platform"},
		}},
		Pods: rig.pods, Stopper: rig.stopper, Alerts: rig.alerts, Threshold: 1,
		ExemptProjects: []string{"proj-jfp7kx46kb"}, ProtectedNamespaces: []string{"excalibase-platform"},
		Now: func() time.Time { return rig.now },
	})
	rig.guard.Sweep(context.Background())
	if len(rig.pods.asked) != 0 || len(rig.alerts.calls) != 0 {
		t.Fatalf("protected namespaces were read or alerted on: asked %v alerts %v", rig.pods.asked, rig.alerts.calls)
	}
}

func TestCrashGuard_AFailedStopIsRetriedOnTheNextSweep(t *testing.T) {
	rig := newGuardRig(t)
	rig.stopper.err = errors.New("pause failed")
	rig.sweepAt(0, 0, "", 0)
	rig.sweepAt(1, 12, "Error", 1)
	rig.stopper.err = nil
	rig.sweepAt(1, 12, "Error", 1)
	if len(rig.stopper.calls) != 2 {
		t.Fatalf("calls = %d, want the stop retried", len(rig.stopper.calls))
	}
	if len(rig.mail.stopped) != 1 {
		t.Errorf("mail sent %d times, want once, for the stop that happened", len(rig.mail.stopped))
	}
}
