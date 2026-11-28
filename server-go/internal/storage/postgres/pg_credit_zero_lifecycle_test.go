//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/creditzero"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// The credit pauses and resumes through the real PauseService and AppDeployService, over the real
// platform database. Only the cluster and the backup store are scripted.

type scriptedDatabasePauser struct {
	pauseErr  error
	stopped   bool
	resumed   int
	paused    int
	stopCalls int
}

func (p *scriptedDatabasePauser) StopReplication(context.Context, string, string) error { return nil }

func (p *scriptedDatabasePauser) Pause(context.Context, string, string) error {
	p.paused++
	if p.pauseErr != nil {
		return p.pauseErr
	}
	p.stopped = true
	return nil
}

func (p *scriptedDatabasePauser) Resume(context.Context, string, string) error {
	p.resumed++
	p.stopped = false
	return nil
}

func (p *scriptedDatabasePauser) WorkloadStopped(context.Context, string, string) (bool, error) {
	return p.stopped, nil
}

type scriptedBackups struct{ failBackup bool }

func (b *scriptedBackups) BackupsConfigured(string) (bool, error) { return true, nil }
func (b *scriptedBackups) TriggerManualBackup(context.Context, string) (map[string]interface{}, error) {
	return map[string]interface{}{"id": "bk-1", "status": "IN_PROGRESS"}, nil
}
func (b *scriptedBackups) LatestBackupID(context.Context, string) (string, error) { return "bk-1", nil }
func (b *scriptedBackups) BackupStatus(context.Context, string, string) (string, error) {
	if b.failBackup {
		return "FAILED", nil
	}
	return "COMPLETED", nil
}

type zeroTiers struct{}

func (zeroTiers) TierConfig(_ context.Context, tier domain.TierType) (config.TierConfig, error) {
	return config.TierConfig{Instances: 3, MaxApps: 20}, nil
}

type zeroPlan struct{}

func (zeroPlan) ProjectPlanTier(context.Context, string) (domain.TierType, error) {
	return domain.Free, nil
}

// serviceWorkloads is what the platform wires: the credit's pauses and resumes are the lifecycle services' own.
type serviceWorkloads struct {
	databases *service.PauseService
	apps      *service.AppDeployService
	// beforePause runs as a pause begins, before the lifecycle checks the service under its lease.
	beforePause func(row creditzero.Paused)
}

func (w serviceWorkloads) Pause(ctx context.Context, row creditzero.Paused, admitted func() error) error {
	if w.beforePause != nil {
		w.beforePause(row)
	}
	var err error
	if row.Kind == creditzero.KindApp {
		_, err = w.apps.PauseAppForCredit(ctx, row.ProjectID, row.ServiceID, admitted)
		if errors.Is(err, service.ErrAppNotRunning) && !errors.Is(err, creditzero.ErrNotCredits) {
			return fmt.Errorf("%w: %v", creditzero.ErrNotCredits, err)
		}
		return err
	}
	err = w.databases.PauseIdle(ctx, row.ProjectID, domain.PauseReasonCredits, admitted)
	if errors.Is(err, service.ErrNoLongerIdle) && !errors.Is(err, creditzero.ErrNotCredits) {
		return fmt.Errorf("%w: %v", creditzero.ErrNotCredits, err)
	}
	return err
}

func (w serviceWorkloads) Resume(ctx context.Context, row creditzero.Paused) error {
	if row.Kind == creditzero.KindApp {
		_, err := w.apps.ResumeApp(ctx, row.ProjectID, row.ServiceID, "system:credits")
		return err
	}
	return w.databases.Resume(ctx, row.ProjectID)
}

type lifecycleRig struct {
	store    *Store
	org      string
	pauser   *scriptedDatabasePauser
	backups  *scriptedBackups
	kube     *k8s.MockClient
	workload serviceWorkloads
	enforcer *creditzero.Enforcer
	zero     *CreditZeroStore
}

func newLifecycleRig(t *testing.T, person string) *lifecycleRig {
	t.Helper()
	store, org := zeroStore(t, person)
	fund(t, store, org, 5)
	pauser := &scriptedDatabasePauser{}
	backups := &scriptedBackups{}
	pauses := service.NewPauseService(service.PauseServiceConfig{
		Instances: store,
		Pausers:   map[domain.DeploymentMode]provisioner.Pauser{domain.ModeK8s: pauser},
		Backups:   backups,
		Poller:    provisioner.NewPoller(time.Millisecond, 2*time.Second),
	})
	kube := k8s.NewMockClient()
	kube.PausedReplicas = 1
	kube.Capacity = k8s.ClusterCapacity{
		AllocatableCPUMilli: 64000, AllocatableMemBytes: 256 << 30,
		Nodes: []k8s.NodeCapacity{{Name: "n1", AllocatableCPUMilli: 64000, AllocatableMemBytes: 256 << 30}},
	}
	apps := service.NewAppDeployService(apphost.NewPostgresAppStore(store.db), apphost.NewPostgresDeployStore(store.db), kube, store, nil, k8s.AppRenderOptions{})
	apps.SetPlanTiers(zeroPlan{})
	apps.SetNamespaceQuotaTiers(zeroTiers{})
	rig := &lifecycleRig{store: store, org: org, pauser: pauser, backups: backups, kube: kube, zero: NewCreditZeroStore(store)}
	rig.workload = serviceWorkloads{databases: pauses, apps: apps}
	rig.enforcer = zeroEnforcer(store, rig.workload, zeroGate())
	return rig
}

// outOfCreditPastGrace drains the org and starts its grace a week and a minute ago.
func (r *lifecycleRig) outOfCreditPastGrace(t *testing.T) {
	t.Helper()
	drain(t, r.store, r.org)
	if err := r.zero.MarkZero(context.Background(), r.org); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.db.Exec(`UPDATE credit_zero_orgs SET since = now() - interval '7 days 1 minute' WHERE org_id = $1`, r.org); err != nil {
		t.Fatal(err)
	}
}

func (r *lifecycleRig) dbStatus(t *testing.T, project string) (status, reason string) {
	t.Helper()
	if err := r.store.db.QueryRow(`SELECT status, COALESCE(pause_reason, '') FROM database_instances WHERE project_id = $1`, project).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	return status, reason
}

func (r *lifecycleRig) rows(t *testing.T) []creditzero.Paused {
	t.Helper()
	rows, err := r.zero.Paused(context.Background(), r.org)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// bigDatabase is over the 1000m allowance on its own, so it is the one credit pauses.
func (r *lifecycleRig) bigDatabase(t *testing.T, project string) {
	t.Helper()
	newDatabase(t, r.store, project, r.org)
	if _, err := r.store.db.Exec(`UPDATE database_instances SET reserved_cpu_milli = 1500, reserved_memory_bytes = $2, created_at = now() WHERE project_id = $1`, project, int64(1536)<<20); err != nil {
		t.Fatal(err)
	}
}

func TestAPauseThatFailsAfterTheBackupKeepsItsRowAndCreditReturningWhilePausingResumesOnlyOnceItSettles(t *testing.T) {
	rig := newLifecycleRig(t, "zl1")
	rig.bigDatabase(t, "proj-zl1")
	rig.outOfCreditPastGrace(t)
	ctx := context.Background()

	rig.pauser.pauseErr = errors.New("the database pod would not stop")
	if err := rig.enforcer.Pass(ctx); err == nil {
		t.Fatal("the failed pause is reported")
	}
	if status, reason := rig.dbStatus(t, "proj-zl1"); status != "PAUSING" || reason != domain.PauseReasonCredits {
		t.Fatalf("the pause failed after the backup and left the database PAUSING for credit: %s %s", status, reason)
	}
	if rows := rig.rows(t); len(rows) != 1 || rows[0].State != creditzero.HoldBusy {
		t.Fatalf("a failed pause keeps its row, and PAUSING reads as busy: %+v", rows)
	}

	fund(t, rig.store, rig.org, 5)
	if err := rig.enforcer.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if rows := rig.rows(t); len(rows) != 1 {
		t.Fatalf("credit came back while it was still PAUSING: resuming then is a silent no-op, so the row must stay: %+v", rows)
	}
	if rig.pauser.resumed != 0 {
		t.Fatal("nothing was resumed yet")
	}

	rig.pauser.pauseErr = nil
	if err := rig.workload.databases.PauseStuck(ctx, "proj-zl1", domain.PauseReasonCredits, func() error { return nil }); err != nil {
		t.Fatalf("the stuck-pause retry finishes it: %v", err)
	}
	if status, reason := rig.dbStatus(t, "proj-zl1"); status != "PAUSED" || reason != domain.PauseReasonCredits {
		t.Fatalf("%s %s", status, reason)
	}
	if err := rig.enforcer.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if status, _ := rig.dbStatus(t, "proj-zl1"); status != "ACTIVE" || rig.pauser.resumed != 1 {
		t.Fatalf("the next pass resumes what the retry finished: %s, resumed %d", status, rig.pauser.resumed)
	}
	if rows := rig.rows(t); len(rows) != 0 {
		t.Fatalf("and only then forgets it: %+v", rows)
	}
}

func TestAPauseThatFailedBeforeTheBackupFinishedLeavesARowThatIsDroppedNotResumed(t *testing.T) {
	rig := newLifecycleRig(t, "zl2")
	rig.bigDatabase(t, "proj-zl2")
	rig.outOfCreditPastGrace(t)
	ctx := context.Background()

	rig.backups.failBackup = true
	if err := rig.enforcer.Pass(ctx); err == nil {
		t.Fatal("the failed pause is reported")
	}
	if status, _ := rig.dbStatus(t, "proj-zl2"); status != "ACTIVE" {
		t.Fatalf("a database that was not backed up stays running: %s", status)
	}
	fund(t, rig.store, rig.org, 5)
	if err := rig.enforcer.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if rows := rig.rows(t); len(rows) != 0 {
		t.Fatalf("the row of a service that is running after all is dropped: %+v", rows)
	}
	if rig.pauser.resumed != 0 || rig.pauser.paused != 0 {
		t.Fatalf("credit neither paused nor resumed anything: paused %d resumed %d", rig.pauser.paused, rig.pauser.resumed)
	}
}

func (r *lifecycleRig) app(t *testing.T, project, id string) {
	t.Helper()
	app := &apphost.App{ID: id, ProjectID: project, Name: id, Image: "ghcr.io/acme/web:1", Port: 8080, Replicas: 1, Tier: domain.Free, Status: apphost.StatusRunning}
	if err := apphost.NewPostgresAppStore(r.store.db).Create(app, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.db.Exec(`UPDATE apps SET status = 'ACTIVE', doc = jsonb_set(doc, '{status}', '"ACTIVE"') WHERE project_id = $1 AND id = $2`, project, id); err != nil {
		t.Fatal(err)
	}
}

func (r *lifecycleRig) appStatus(t *testing.T, project, id string) string {
	t.Helper()
	var status string
	if err := r.store.db.QueryRow(`SELECT status FROM apps WHERE project_id = $1 AND id = $2`, project, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func TestCreditPausesARunningAppAndResumesItThroughTheRealLifecycle(t *testing.T) {
	rig := newLifecycleRig(t, "zl3")
	newDatabase(t, rig.store, "proj-zl3", rig.org)
	rig.app(t, "proj-zl3", "app-big")
	if _, err := rig.store.db.Exec(`UPDATE apps SET doc = jsonb_set(doc, '{replicas}', '6') WHERE id = 'app-big'`); err != nil {
		t.Fatal(err)
	}
	rig.outOfCreditPastGrace(t)
	ctx := context.Background()
	if err := rig.enforcer.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if got := rig.appStatus(t, "proj-zl3", "app-big"); got != apphost.StatusStopped {
		t.Fatalf("the app is stopped through the lifecycle: %s", got)
	}
	if rows := rig.rows(t); len(rows) != 1 || rows[0].State != creditzero.HoldPaused {
		t.Fatalf("%+v", rows)
	}
	fund(t, rig.store, rig.org, 5)
	if err := rig.enforcer.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if got := rig.appStatus(t, "proj-zl3", "app-big"); got != apphost.StatusRunning {
		t.Fatalf("credit returned, so the app runs again: %s", got)
	}
	if rows := rig.rows(t); len(rows) != 0 {
		t.Fatalf("%+v", rows)
	}
}

func TestAUserStopBetweenTheReadAndTheCreditPauseIsNeverRestartedByCredit(t *testing.T) {
	rig := newLifecycleRig(t, "zl4")
	newDatabase(t, rig.store, "proj-zl4", rig.org)
	rig.app(t, "proj-zl4", "app-big")
	if _, err := rig.store.db.Exec(`UPDATE apps SET doc = jsonb_set(doc, '{replicas}', '6') WHERE id = 'app-big'`); err != nil {
		t.Fatal(err)
	}
	rig.outOfCreditPastGrace(t)
	ctx := context.Background()
	rig.workload.beforePause = func(row creditzero.Paused) {
		if row.Kind != creditzero.KindApp {
			return
		}
		if _, err := rig.workload.apps.PauseApp(ctx, row.ProjectID, row.ServiceID); err != nil {
			t.Errorf("the user's own stop: %v", err)
		}
	}
	rig.enforcer = zeroEnforcer(rig.store, rig.workload, zeroGate())
	if err := rig.enforcer.Pass(ctx); err != nil {
		t.Fatalf("an app the user already stopped is not a failure: %v", err)
	}
	for _, row := range rig.rows(t) {
		if row.Kind == creditzero.KindApp {
			t.Fatalf("credit did not stop the app, so it holds no row for it: %+v", row)
		}
	}
	fund(t, rig.store, rig.org, 5)
	if err := rig.enforcer.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if got := rig.appStatus(t, "proj-zl4", "app-big"); got != apphost.StatusStopped {
		t.Fatalf("what the user stopped stays stopped when credit returns: %s", got)
	}
}
