package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
)

// purgeRetryLab is a project whose teardown left its backups pending delete,
// with the retry clock under the test's hand.
type purgeRetryLab struct {
	svc     *ProvisioningService
	deleter *fakeObjectDeleter
	now     time.Time
}

func newPurgeRetryLab(t *testing.T, mode domain.DeploymentMode, flags ...features.Feature) *purgeRetryLab {
	t.Helper()
	svc, store, _ := setupProvisioningTest(t)
	key := testPurgeProject + "/cloud/wals/x.gz"
	if mode == domain.ModeDocker {
		key = "backups/" + testPurgeProject + "/wals/x.gz"
	}
	deleter := newFakeObjectDeleter(key)
	svc.SetBackupPurger(newTestPurger(deleter))
	svc.SetFeatures(features.NewStatic(flags...))
	if err := store.Create(&domain.DatabaseInstance{ProjectID: testPurgeProject, OrgID: "org1", DBType: domain.PostgreSQL,
		DeploymentMode: mode, Status: string(domain.StatusBackupsPendingDelete)}); err != nil {
		t.Fatal(err)
	}
	lab := &purgeRetryLab{svc: svc, deleter: deleter, now: time.Now()}
	svc.rollbackRetries.now = func() time.Time { return lab.now }
	return lab
}

func (l *purgeRetryLab) pass() []string { return l.svc.RetryFailedRollbacks(context.Background()) }

func (l *purgeRetryLab) later() { l.now = l.now.Add(25 * time.Hour) }

func (l *purgeRetryLab) rowGone() bool {
	inst, _ := l.svc.store.FindByProjectID(testPurgeProject)
	return inst == nil
}

func TestPurgeRetry_OffWhileTheFlagIsOff(t *testing.T) {
	lab := newPurgeRetryLab(t, domain.ModeK8s)

	if done := lab.pass(); len(done) != 0 || lab.deleter.listCalls != 0 || lab.rowGone() {
		t.Fatalf("flag off touched the pending purge: done %v, lists %d", done, lab.deleter.listCalls)
	}
}

func TestPurgeRetry_PurgesAndRemovesTheRowOnBothModes(t *testing.T) {
	for _, mode := range []domain.DeploymentMode{domain.ModeK8s, domain.ModeDocker} {
		t.Run(string(mode), func(t *testing.T) {
			lab := newPurgeRetryLab(t, mode, features.SelfHeal)

			if done := lab.pass(); len(done) != 1 || done[0] != testPurgeProject {
				t.Fatalf("finished %v", done)
			}
			if !lab.rowGone() || len(lab.deleter.remaining()) != 0 {
				t.Fatalf("row gone %v, objects left %v", lab.rowGone(), lab.deleter.remaining())
			}
		})
	}
}

func TestPurgeRetry_BacksOffWhileTheStoreStaysDown(t *testing.T) {
	lab := newPurgeRetryLab(t, domain.ModeK8s, features.SelfHeal)
	lab.deleter.deleteErr = errors.New("r2 unavailable")

	lab.pass()
	attempts := lab.deleter.listCalls
	lab.pass()
	if attempts == 0 || lab.deleter.listCalls != attempts {
		t.Fatalf("second pass in the same window retried: lists %d then %d", attempts, lab.deleter.listCalls)
	}
	if lab.rowGone() {
		t.Fatal("a failed purge dropped its retry marker")
	}

	lab.deleter.deleteErr = nil
	lab.later()
	if done := lab.pass(); len(done) != 1 || !lab.rowGone() {
		t.Fatalf("after the backoff and a healed store: finished %v, row gone %v", done, lab.rowGone())
	}
}

func TestPurgeRetry_WaitsForTheProjectLease(t *testing.T) {
	lab := newPurgeRetryLab(t, domain.ModeK8s, features.SelfHeal)
	release, claimed, err := lab.svc.claimer().Claim(context.Background(), testPurgeProject, OperationRestore)
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}

	if done := lab.pass(); len(done) != 0 || lab.deleter.listCalls != 0 {
		t.Fatalf("purged under another operation's lease: %v, lists %d", done, lab.deleter.listCalls)
	}
	release()
	if done := lab.pass(); len(done) != 1 {
		t.Fatalf("after the lease was released: finished %v", done)
	}
}

func TestPurgeRetry_SkipsAModeWithNoBackups(t *testing.T) {
	lab := newPurgeRetryLab(t, domain.DeploymentMode("unwired"), features.SelfHeal)

	if done := lab.pass(); len(done) != 1 || !lab.rowGone() || lab.deleter.listCalls != 0 {
		t.Fatalf("finished %v, row gone %v, lists %d", done, lab.rowGone(), lab.deleter.listCalls)
	}
}

func TestPurgeRetry_AlertsStaffAfterTheLimitAndClosesOnSuccess(t *testing.T) {
	lab := newPurgeRetryLab(t, domain.ModeK8s, features.SelfHeal)
	alerts := NewAlertingService(t.TempDir())
	notifier := &mailCountingNotifier{}
	lab.svc.SetRetryEscalator(NewRetryEscalator(alerts, notifier, 2))
	lab.deleter.deleteErr = errors.New("r2 unavailable")

	lab.pass()
	if open := openPlatformAlerts(t, alerts); len(open) != 0 {
		t.Fatalf("alert after the first failure: %v", open)
	}
	lab.later()
	lab.pass()
	if open := openPlatformAlerts(t, alerts); !open[RollbackRetryMetricPrefix+testPurgeProject] || notifier.raised != 1 {
		t.Fatalf("after the second failure: open %v, mails %d", open, notifier.raised)
	}

	lab.deleter.deleteErr = nil
	lab.later()
	lab.pass()
	if open := openPlatformAlerts(t, alerts); len(open) != 0 || notifier.cleared != 1 {
		t.Fatalf("after success: open %v, resolved mails %d", open, notifier.cleared)
	}
}
