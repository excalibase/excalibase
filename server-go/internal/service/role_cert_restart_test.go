package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/tenantcert"
)

const restartAlertAfter = 30 * time.Minute

// watcherPodOn puts the project's watcher pod in the namespace, running the
// certificate with the given fingerprint.
func (h *renewalHarness) watcherPodOn(fingerprint string) {
	h.kube.Pods["ns-renew"] = []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{
		Name:        "cdc-watcher-0",
		Labels:      map[string]string{provisioner.WatcherComponentLabel: provisioner.WatcherComponentValue},
		Annotations: map[string]string{provisioner.WatcherCertAnnotation: fingerprint},
	}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}}
}

func (h *renewalHarness) vaultWatcherFingerprint(t *testing.T) string {
	t.Helper()
	material, err := tenantcert.FromRecord(h.vault.data[vaultCredentialPath(renewProject, roleWatcher)])
	if err != nil {
		t.Fatal(err)
	}
	return provisioner.WatcherCertFingerprint(material)
}

// A restart that failed once leaves the new certificate in vault; the next
// pass must still put the watcher onto it (EXC-887).
func TestAFailedWatcherRestartIsRetriedOnTheNextPass(t *testing.T) {
	h := dueHarness(t)
	h.watcherPodOn("fingerprint-of-the-old-certificate")
	attempts := 0
	h.renewer.cfg.RestartReplication = func(_ context.Context, inst *domain.DatabaseInstance) error {
		attempts++
		if attempts == 1 {
			return errors.New("per-pass deadline exceeded")
		}
		h.watcherPodOn(h.vaultWatcherFingerprint(t))
		return nil
	}

	if report := h.renewer.RenewDue(context.Background()); len(report.Failed) != 1 {
		t.Fatalf("first pass %+v", report)
	}
	report := h.renewer.RenewDue(context.Background())
	if attempts != 2 || len(report.Failed) != 0 {
		t.Fatalf("second pass: attempts %d, report %+v", attempts, report)
	}
	h.renewer.RenewDue(context.Background())
	if attempts != 2 {
		t.Errorf("a watcher already on the certificate was restarted again: %d attempts", attempts)
	}
}

func TestAWatcherOnTheCurrentCertificateIsNotRestarted(t *testing.T) {
	h := newRenewalHarness(t, "ACTIVE")
	h.watcherPodOn(h.vaultWatcherFingerprint(t))
	if report := h.renewer.RenewDue(context.Background()); len(report.Failed) != 0 || len(h.restarted) != 0 {
		t.Fatalf("report %+v, restarted %v", report, h.restarted)
	}
}

func TestAPausedProjectsStaleWatcherIsNotRestarted(t *testing.T) {
	h := newRenewalHarness(t, string(domain.StatusPaused))
	h.watcherPodOn("fingerprint-of-the-old-certificate")
	h.renewer.RenewDue(context.Background())
	if len(h.restarted) != 0 {
		t.Errorf("restarted a paused project: %v", h.restarted)
	}
}

func TestAnUnreadablePodListIsReported(t *testing.T) {
	h := newRenewalHarness(t, "ACTIVE")
	h.kube.GetPodsError = errors.New("api server down")
	if report := h.renewer.RenewDue(context.Background()); len(report.Failed) != 1 {
		t.Fatalf("report %+v", report)
	}
}

func renewalAlertHarness(t *testing.T) (*renewalHarness, *AlertingService, *walNotifier) {
	t.Helper()
	h := dueHarness(t)
	h.watcherPodOn("fingerprint-of-the-old-certificate")
	alerts := NewAlertingService(t.TempDir())
	notifier := &walNotifier{}
	h.renewer.SetAlerts(alerts, notifier, restartAlertAfter)
	return h, alerts, notifier
}

// A watcher that cannot be restarted pass after pass is an alert, once, and it
// resolves when the restart works.
func TestAWatcherThatKeepsFailingToRestartRaisesAnAlert(t *testing.T) {
	h, alerts, notifier := renewalAlertHarness(t)
	failing := true
	h.renewer.cfg.RestartReplication = func(context.Context, *domain.DatabaseInstance) error {
		if failing {
			return errors.New("helm unavailable")
		}
		h.watcherPodOn(h.vaultWatcherFingerprint(t))
		return nil
	}
	ctx := context.Background()
	h.renewer.RenewDue(ctx)
	if got := alerts.GetActiveAlerts(); len(got) != 0 {
		t.Fatalf("raised on the first failure: %+v", got)
	}
	h.now = h.now.Add(restartAlertAfter + time.Minute)
	h.renewer.RenewDue(ctx)
	open := alerts.GetActiveAlerts()
	if len(open) != 1 || open[0].Metric != "role.cert.renewal:"+renewProject || open[0].ProjectID != "" {
		t.Fatalf("open alerts = %+v", open)
	}
	if !strings.Contains(open[0].Message, renewProject) || !strings.Contains(open[0].Message, "helm unavailable") {
		t.Fatalf("message = %q", open[0].Message)
	}
	if len(notifier.changes) != 1 || notifier.changes[0].Raised == nil {
		t.Fatalf("notified %d times, want one raise", len(notifier.changes))
	}

	failing = false
	h.renewer.RenewDue(ctx)
	if got := alerts.GetActiveAlerts(); len(got) != 0 {
		t.Fatalf("still open after the restart worked: %+v", got)
	}
}

// The restart waits for Helm, which waits up to five minutes: the per-project
// 30 s budget would cancel an upgrade half way and leave the release pending.
func TestTheWatcherRestartGetsMoreTimeThanHelmWaits(t *testing.T) {
	h := dueHarness(t)
	var left time.Duration
	h.renewer.cfg.RestartReplication = func(ctx context.Context, _ *domain.DatabaseInstance) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("the restart has no deadline")
		}
		left = time.Until(deadline)
		return nil
	}
	h.renewer.RenewDue(context.Background())
	if left <= k8s.HelmWait {
		t.Errorf("restart budget %s, want more than Helm's %s wait", left, k8s.HelmWait)
	}
}

// Pods that are going away or already gone keep the old fingerprint for good;
// counting them would restart the watcher on every pass.
func TestOnlyLivePodsDecideWhetherTheWatcherIsStale(t *testing.T) {
	gone := metav1.Now()
	cases := map[string]corev1.Pod{
		"terminating": {ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &gone}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		"evicted":     {Status: corev1.PodStatus{Phase: corev1.PodFailed}},
		"completed":   {Status: corev1.PodStatus{Phase: corev1.PodSucceeded}},
	}
	for name, lingering := range cases {
		t.Run(name, func(t *testing.T) {
			h := newRenewalHarness(t, "ACTIVE")
			h.watcherPodOn(h.vaultWatcherFingerprint(t))
			lingering.Name = "old-watcher"
			lingering.Labels = map[string]string{provisioner.WatcherComponentLabel: provisioner.WatcherComponentValue}
			lingering.Annotations = map[string]string{provisioner.WatcherCertAnnotation: "the-old-certificate"}
			h.kube.Pods["ns-renew"] = append(h.kube.Pods["ns-renew"], lingering)
			h.renewer.RenewDue(context.Background())
			if len(h.restarted) != 0 {
				t.Errorf("restarted because of a %s pod", name)
			}
		})
	}
}

func TestAPendingPodOnTheOldCertificateIsStale(t *testing.T) {
	h := newRenewalHarness(t, "ACTIVE")
	h.watcherPodOn("the-old-certificate")
	h.kube.Pods["ns-renew"][0].Status.Phase = corev1.PodPending
	h.renewer.RenewDue(context.Background())
	if len(h.restarted) != 1 {
		t.Errorf("restarts = %v", h.restarted)
	}
}

// Failing, recovering and failing again is a new streak: the alert clock
// starts over instead of counting the time the project was healthy.
func TestARecoveredProjectStartsItsAlertClockOver(t *testing.T) {
	h, alerts, _ := renewalAlertHarness(t)
	failing := true
	h.renewer.cfg.RestartReplication = func(context.Context, *domain.DatabaseInstance) error {
		if failing {
			return errors.New("helm unavailable")
		}
		h.watcherPodOn(h.vaultWatcherFingerprint(t))
		return nil
	}
	ctx := context.Background()
	h.renewer.RenewDue(ctx)
	failing = false
	h.now = h.now.Add(10 * time.Minute)
	h.renewer.RenewDue(ctx)
	h.watcherPodOn("fingerprint-of-the-old-certificate")
	failing = true
	h.now = h.now.Add(restartAlertAfter)
	h.renewer.RenewDue(ctx)
	if got := alerts.GetActiveAlerts(); len(got) != 0 {
		t.Fatalf("alerted on the first failure of a new streak: %+v", got)
	}
}

// A pause holding the lease means the renewer must not put the watcher back:
// replication on a paused database holds its shutdown open (EXC-363).
func TestAProjectAnotherOperationHoldsKeepsItsWatcherForTheNextPass(t *testing.T) {
	h := dueHarness(t)
	h.watcherPodOn("fingerprint-of-the-old-certificate")
	busy := true
	released := false
	inner := h.hold
	h.hold = func(ctx context.Context, id string) (*domain.DatabaseInstance, func(), error) {
		if busy {
			return nil, nil, ErrProjectOperationRunning
		}
		inst, release, err := inner(ctx, id)
		return inst, func() { released = true; release() }, err
	}
	if report := h.renewer.RenewDue(context.Background()); len(report.Failed) != 0 || len(h.restarted) != 0 {
		t.Fatalf("a held project: report %+v, restarted %v", report, h.restarted)
	}
	busy = false
	h.renewer.RenewDue(context.Background())
	if len(h.restarted) != 1 || !released {
		t.Errorf("next pass: restarted %v, lease released %v", h.restarted, released)
	}
}

// The pass reads its projects once at the start; a pause in the minutes
// since is only visible under the lease.
func TestAProjectPausedSinceThePassBeganIsNotRestarted(t *testing.T) {
	h := dueHarness(t)
	h.watcherPodOn("fingerprint-of-the-old-certificate")
	inner := h.hold
	h.hold = func(ctx context.Context, id string) (*domain.DatabaseInstance, func(), error) {
		paused, _ := h.store.FindByProjectID(id)
		paused.Status = string(domain.StatusPaused)
		if err := h.store.Update(paused); err != nil {
			t.Fatal(err)
		}
		return inner(ctx, id)
	}
	if report := h.renewer.RenewDue(context.Background()); len(report.Failed) != 0 || len(h.restarted) != 0 {
		t.Fatalf("report %+v, restarted %v", report, h.restarted)
	}
}

// The restart acts on the project as the lease read it.
func TestTheRestartGetsTheProjectAsTheLeaseReadIt(t *testing.T) {
	h := dueHarness(t)
	var got *domain.DatabaseInstance
	h.renewer.cfg.RestartReplication = func(_ context.Context, inst *domain.DatabaseInstance) error {
		got = inst
		return nil
	}
	h.renewer.RenewDue(context.Background())
	if got == nil || got.Status != string(domain.StatusActive) {
		t.Fatalf("restart got %+v", got)
	}
}

// A redeploy that failed after the old release was removed leaves no watcher
// pod at all; that is a stale watcher, not a healthy project with nothing to compare.
func TestAMissingWatcherOfAProjectThatHasOneIsRestartedAndAlerted(t *testing.T) {
	h, alerts, _ := renewalAlertHarness(t)
	h.kube.Pods["ns-renew"] = nil
	h.kube.Secrets["ns-renew/"+provisioner.WatcherTLSSecretName(renewProject)] = map[string][]byte{"tls.crt": []byte("c")}
	h.renewer.cfg.RestartReplication = func(context.Context, *domain.DatabaseInstance) error {
		return errors.New("install failed")
	}
	ctx := context.Background()
	h.renewer.RenewDue(ctx)
	h.now = h.now.Add(restartAlertAfter + time.Minute)
	report := h.renewer.RenewDue(ctx)
	if len(report.Failed) != 1 || len(alerts.GetActiveAlerts()) != 1 {
		t.Fatalf("report %+v, alerts %+v", report, alerts.GetActiveAlerts())
	}
}

func TestAProjectThatNeverHadAWatcherIsLeftAlone(t *testing.T) {
	h := newRenewalHarness(t, "ACTIVE")
	h.kube.Pods["ns-renew"] = nil
	if report := h.renewer.RenewDue(context.Background()); len(report.Failed) != 0 || len(h.restarted) != 0 {
		t.Fatalf("report %+v, restarted %v", report, h.restarted)
	}
}

// A renewer wired without the lease must fail the project's pass, not panic
// the goroutine that renews every project.
func TestARenewerWithoutALeaseReportsInsteadOfPanicking(t *testing.T) {
	h := dueHarness(t)
	h.renewer.cfg.HoldProject = nil
	report := h.renewer.RenewDue(context.Background())
	if len(report.Failed) != 1 || len(h.restarted) != 0 {
		t.Fatalf("report %+v, restarted %v", report, h.restarted)
	}
}

// A watcher that cannot be redeployed is reported, so the renewer retries it (EXC-887).
func TestAFailedWatcherRedeployIsReported(t *testing.T) {
	h := newRegistrationHarness(t)
	pg := provisioner.NewPostgreSQLProvisioner(h.kube, "/charts/excalibase-watcher")
	pg.SetWatcherImage("excalibase/excalibase-watcher-go:1.0.0")
	h.svc.factory = provisioner.NewFactory(pg)
	h.svc.SetNatsCredentialMinter(NewNatsCredentialMinter(newFakeNatsCredStore()))
	inst := restoredInstance()
	seedWatcherRecord(t, h, inst.ProjectID)

	h.kube.HelmError = errors.New("helm unavailable")
	if err := h.svc.RestartReplication(context.Background(), inst); err == nil {
		t.Fatal("the failed deploy was not reported")
	}

	h.kube.HelmError = nil
	if err := h.svc.RestartReplication(context.Background(), inst); err != nil {
		t.Fatal(err)
	}
}
