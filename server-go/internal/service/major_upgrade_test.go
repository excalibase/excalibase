package service

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// upgradeWorld fakes everything a major upgrade talks to and records the
// order of the calls, so a test reads the sequence back.
type upgradeWorld struct {
	mu    sync.Mutex
	calls []string

	instances storage.InstanceStore
	runs      *memMajorUpgrades
	released  int
	holdErr   error

	blockers     []string
	preflightErr error
	backupErrs   []error
	outcome      UpgradeOutcome
	recovered    UpgradeOutcome
	servingErr   error
	afterWarns   []string
	probeErrs    []error
	failOnce     map[string]error
	statuses     []string
}

func (w *upgradeWorld) record(call string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, call)
}

func (w *upgradeWorld) sequence() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.calls...)
}

func (w *upgradeWorld) step(name string) error {
	w.record(name)
	w.mu.Lock()
	defer w.mu.Unlock()
	err := w.failOnce[name]
	delete(w.failOnce, name)
	return err
}

func (w *upgradeWorld) hold(projectID string, admit func(string) error) (*domain.DatabaseInstance, func(), error) {
	w.record("hold")
	if w.holdErr != nil {
		return nil, nil, w.holdErr
	}
	inst, err := w.instances.FindByProjectID(projectID)
	if err != nil {
		return nil, nil, err
	}
	if err := admit(inst.Status); err != nil {
		return nil, nil, err
	}
	return inst, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.released++
	}, nil
}

func (w *upgradeWorld) HoldForMajorUpgrade(_ context.Context, projectID string, admit func(status string) error) (*domain.DatabaseInstance, func(), error) {
	return w.hold(projectID, admit)
}

func (w *upgradeWorld) StopReplication(context.Context, *domain.DatabaseInstance) error {
	return w.step("stop-replication")
}

func (w *upgradeWorld) RestartReplication(context.Context, *domain.DatabaseInstance) error {
	return w.step("restart-replication")
}

func (w *upgradeWorld) ReapplyCredentials(context.Context, *domain.DatabaseInstance) error {
	return w.step("reapply-credentials")
}

func (w *upgradeWorld) AnnounceDatabaseReplaced(context.Context, string) { w.record("announce") }

func (w *upgradeWorld) Probe(context.Context, string) error {
	w.record("probe")
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.probeErrs) > 0 {
		err := w.probeErrs[0]
		w.probeErrs = w.probeErrs[1:]
		return err
	}
	return nil
}

func (w *upgradeWorld) ProjectStatusChanged(_, status string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.statuses = append(w.statuses, status)
}

func (w *upgradeWorld) Preflight(_ context.Context, _ *domain.DatabaseInstance, target string) ([]string, error) {
	w.record("preflight:" + target)
	return w.blockers, w.preflightErr
}

func (w *upgradeWorld) TakeBackup(_ context.Context, inst *domain.DatabaseInstance) (string, error) {
	w.record("backup:" + inst.PostgresVersion)
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.backupErrs) > 0 {
		err := w.backupErrs[0]
		w.backupErrs = w.backupErrs[1:]
		if err != nil {
			return "", err
		}
	}
	return "backup-" + inst.PostgresVersion, nil
}

func (w *upgradeWorld) Convert(_ context.Context, _ *domain.DatabaseInstance, run domain.MajorUpgrade, progress UpgradeProgress) UpgradeOutcome {
	w.record("convert:" + run.ToVersion)
	progress.Step(domain.MajorUpgradeStepConverting)
	progress.WritesStopped(time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC))
	if w.outcome.Converted {
		if err := progress.Converted(time.Date(2026, 10, 29, 10, 5, 0, 0, time.UTC)); err != nil {
			return UpgradeOutcome{Cause: err}
		}
	}
	return w.outcome
}

func (w *upgradeWorld) Recover(_ context.Context, _ *domain.DatabaseInstance, run domain.MajorUpgrade, progress UpgradeProgress) UpgradeOutcome {
	w.record("recover:" + run.ToVersion)
	if w.recovered.Converted {
		if err := progress.Converted(time.Date(2026, 10, 29, 10, 5, 0, 0, time.UTC)); err != nil {
			return UpgradeOutcome{Cause: err}
		}
	}
	return w.recovered
}

func (w *upgradeWorld) WaitServing(context.Context, *domain.DatabaseInstance) error {
	w.record("wait-serving")
	return w.servingErr
}

func (w *upgradeWorld) AfterSteps(context.Context, *domain.DatabaseInstance) []string {
	w.record("after-steps")
	return w.afterWarns
}

func (w *upgradeWorld) Cleanup(context.Context, *domain.DatabaseInstance, domain.MajorUpgrade) {
	w.record("cleanup")
}

// memMajorUpgrades is MajorUpgradeStore in memory; converting moves the
// project's version in the instance store, as the real one does.
type memMajorUpgrades struct {
	mu        sync.Mutex
	runs      map[string]*domain.MajorUpgrade
	instances storage.InstanceStore
	markErr   error
}

func newMemMajorUpgrades(instances storage.InstanceStore) *memMajorUpgrades {
	return &memMajorUpgrades{runs: map[string]*domain.MajorUpgrade{}, instances: instances}
}

func (m *memMajorUpgrades) StartMajorUpgrade(_ context.Context, run *domain.MajorUpgrade) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.runs {
		if existing.ProjectID == run.ProjectID && existing.Status == domain.MajorUpgradeRunning {
			return storage.ErrMajorUpgradeRunning
		}
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now().UTC()
	}
	run.Status = domain.MajorUpgradeRunning
	run.HeartbeatAt = run.StartedAt
	stored := *run
	m.runs[run.ID] = &stored
	return nil
}

func (m *memMajorUpgrades) UpdateMajorUpgrade(_ context.Context, run *domain.MajorUpgrade) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, ok := m.runs[run.ID]
	if !ok || stored.Status != domain.MajorUpgradeRunning {
		return storage.ErrMajorUpgradeNotRunning
	}
	converted, convertedAt, heartbeat := stored.Converted, stored.ConvertedAt, stored.HeartbeatAt
	*stored = *run
	stored.Converted, stored.ConvertedAt, stored.HeartbeatAt = converted, convertedAt, heartbeat
	return nil
}

func (m *memMajorUpgrades) TouchMajorUpgrade(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, ok := m.runs[id]
	if !ok || stored.Status != domain.MajorUpgradeRunning {
		return storage.ErrMajorUpgradeNotRunning
	}
	stored.HeartbeatAt = at
	return nil
}

func (m *memMajorUpgrades) MarkMajorUpgradeConverted(_ context.Context, id, projectID, toVersion string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.markErr != nil {
		return m.markErr
	}
	stored, ok := m.runs[id]
	if !ok || stored.Converted || stored.Status != domain.MajorUpgradeRunning {
		return storage.ErrMajorUpgradeNotRunning
	}
	stored.Converted = true
	stored.ConvertedAt = &at
	inst, err := m.instances.FindByProjectID(projectID)
	if err != nil {
		return err
	}
	inst.PostgresVersion = toVersion
	return m.instances.Update(inst)
}

func (m *memMajorUpgrades) LatestMajorUpgrade(_ context.Context, projectID string) (*domain.MajorUpgrade, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest *domain.MajorUpgrade
	for _, run := range m.runs {
		if run.ProjectID == projectID && (latest == nil || run.StartedAt.After(latest.StartedAt)) {
			copied := *run
			latest = &copied
		}
	}
	return latest, nil
}

func (m *memMajorUpgrades) ConvertedMajorUpgrades(_ context.Context, projectID string) ([]domain.MajorUpgrade, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.MajorUpgrade{}
	for _, run := range m.runs {
		if run.ProjectID == projectID && run.Converted {
			out = append(out, *run)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ConvertedAt.Before(*out[j].ConvertedAt) })
	return out, nil
}

func (m *memMajorUpgrades) RunningMajorUpgrades(context.Context) ([]domain.MajorUpgrade, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.MajorUpgrade{}
	for _, run := range m.runs {
		if run.Status == domain.MajorUpgradeRunning {
			out = append(out, *run)
		}
	}
	return out, nil
}

func upgradeProject() *domain.DatabaseInstance {
	return &domain.DatabaseInstance{
		ProjectID: "proj-up", ProjectName: "proj-up", OrgID: "org", DBType: domain.PostgreSQL,
		DeploymentMode: domain.ModeK8s, Status: string(domain.StatusActive), PostgresVersion: "16",
		BackupEnabled: boolPtr(true),
	}
}

func newUpgradeWorld(t *testing.T, project *domain.DatabaseInstance) (*upgradeWorld, *MajorUpgradeService) {
	t.Helper()
	t.Cleanup(config.PublishPostgresCatalogForTest())
	instances, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := instances.Create(project); err != nil {
		t.Fatal(err)
	}
	w := &upgradeWorld{instances: instances, runs: newMemMajorUpgrades(instances), outcome: UpgradeOutcome{Converted: true}}
	svc := NewMajorUpgradeService(MajorUpgradeConfig{
		Runs: w.runs, Instances: instances, Projects: w, Probe: w,
		Engines:   map[domain.DeploymentMode]MajorUpgrader{domain.ModeK8s: w, domain.ModeDocker: w},
		Backups:   func(*domain.DatabaseInstance) bool { return true },
		Observers: []StatusObserver{w},
		NewID:     func() string { return "mu-test" },
	})
	svc.runSync = true
	return w, svc
}

func confirmed(target string) domain.MajorUpgradeRequest {
	return domain.MajorUpgradeRequest{TargetVersion: target, Confirm: true}
}

func projectRow(t *testing.T, w *upgradeWorld) *domain.DatabaseInstance {
	t.Helper()
	inst, err := w.instances.FindByProjectID("proj-up")
	if err != nil {
		t.Fatal(err)
	}
	return inst
}

func TestMajorUpgrade_CompletesInOrderAndRecordsTheNewMajor(t *testing.T) {
	w, svc := newUpgradeWorld(t, upgradeProject())
	w.afterWarns = []string{"statistics were not refreshed"}

	run, err := svc.Start(context.Background(), "proj-up", confirmed("17"), "user-1")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if run.ID != "mu-test" || run.FromVersion != "16" || run.ToVersion != "17" {
		t.Fatalf("run = %+v", run)
	}
	want := []string{"hold", "preflight:17", "backup:16", "stop-replication", "convert:17", "wait-serving",
		"reapply-credentials", "probe", "restart-replication", "announce", "after-steps", "backup:17", "cleanup"}
	if got := w.sequence(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("calls =\n%v\nwant\n%v", got, want)
	}
	latest, _ := w.runs.LatestMajorUpgrade(context.Background(), "proj-up")
	if latest.Status != domain.MajorUpgradeCompleted || !latest.Converted || latest.SafetyBackupID != "backup-16" ||
		latest.FinishedAt == nil || latest.WritesStoppedAt == nil || len(latest.Warnings) != 1 {
		t.Fatalf("record = %+v", latest)
	}
	inst := projectRow(t, w)
	if inst.PostgresVersion != "17" || inst.Status != string(domain.StatusActive) || inst.CurrentStep != "" {
		t.Fatalf("project = version %s status %s step %q", inst.PostgresVersion, inst.Status, inst.CurrentStep)
	}
	if w.released != 1 {
		t.Fatalf("lease released %d times, want 1", w.released)
	}
	if len(w.statuses) == 0 || w.statuses[0] != string(domain.StatusUpgrading) || w.statuses[len(w.statuses)-1] != string(domain.StatusActive) {
		t.Fatalf("observed statuses = %v", w.statuses)
	}
}

func TestMajorUpgrade_RefusesBeforeAnythingChanges(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*domain.DatabaseInstance)
		req    domain.MajorUpgradeRequest
		world  func(*upgradeWorld)
		want   error
	}{
		{"unconfirmed", nil, domain.MajorUpgradeRequest{TargetVersion: "17"}, nil, domain.ErrMajorUpgradeUnconfirmed},
		{"not a newer major", nil, confirmed("16"), nil, ErrMajorUpgradeTargetNotOffered},
		{"below sixteen", func(i *domain.DatabaseInstance) { i.PostgresVersion = "14" }, confirmed("15"), nil, ErrMajorUpgradeTargetNotOffered},
		{"mysql", func(i *domain.DatabaseInstance) { i.DBType = domain.MySQL }, confirmed("17"), nil, ErrMajorUpgradeUnsupported},
		{"documentdb", func(i *domain.DatabaseInstance) { i.DocumentDB = true }, confirmed("17"), nil, ErrMajorUpgradeUnsupported},
		{"no database", func(i *domain.DatabaseInstance) { i.NoDatabase = true }, confirmed("17"), nil, ErrMajorUpgradeUnsupported},
		{"backups off", func(i *domain.DatabaseInstance) { i.BackupEnabled = boolPtr(false) }, confirmed("17"), nil, ErrMajorUpgradeNeedsBackups},
		{"paused", func(i *domain.DatabaseInstance) { i.Status = string(domain.StatusPaused) }, confirmed("17"), nil, ErrProjectNotActive},
		{"busy", nil, confirmed("17"), func(w *upgradeWorld) { w.holdErr = ErrProjectOperationRunning }, ErrProjectOperationRunning},
		{"blocked", nil, confirmed("17"), func(w *upgradeWorld) { w.blockers = []string{"prepared transactions"} }, ErrMajorUpgradeBlocked},
		{"cannot check", nil, confirmed("17"), func(w *upgradeWorld) { w.preflightErr = errors.New("exec refused") }, ErrMajorUpgradeCheckUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			project := upgradeProject()
			if tc.mutate != nil {
				tc.mutate(project)
			}
			w, svc := newUpgradeWorld(t, project)
			if tc.world != nil {
				tc.world(w)
			}
			_, err := svc.Start(context.Background(), "proj-up", tc.req, "user-1")
			if !errors.Is(err, tc.want) {
				t.Fatalf("Start = %v, want %v", err, tc.want)
			}
			for _, call := range w.sequence() {
				if strings.HasPrefix(call, "backup") || strings.HasPrefix(call, "convert") {
					t.Fatalf("a refused upgrade still ran %s", call)
				}
			}
			if latest, _ := w.runs.LatestMajorUpgrade(context.Background(), "proj-up"); latest != nil {
				t.Fatalf("a refused upgrade left a record: %+v", latest)
			}
			if w.released != 0 && projectRow(t, w).Status != project.Status {
				t.Fatalf("status changed to %s", projectRow(t, w).Status)
			}
		})
	}
}

func TestMajorUpgrade_BlockedErrorNamesEveryBlocker(t *testing.T) {
	w, svc := newUpgradeWorld(t, upgradeProject())
	w.blockers = []string{"a", "b"}
	_, err := svc.Start(context.Background(), "proj-up", confirmed("17"), "user-1")
	var blocked *MajorUpgradeBlockedError
	if !errors.As(err, &blocked) || len(blocked.Blockers) != 2 {
		t.Fatalf("err = %v", err)
	}
	if w.released != 1 {
		t.Fatalf("lease released %d times", w.released)
	}
}

func TestMajorUpgrade_BackupFailureChangesNothing(t *testing.T) {
	w, svc := newUpgradeWorld(t, upgradeProject())
	w.backupErrs = []error{errors.New("bucket unreachable")}
	if _, err := svc.Start(context.Background(), "proj-up", confirmed("17"), "user-1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	latest, _ := w.runs.LatestMajorUpgrade(context.Background(), "proj-up")
	if latest.Status != domain.MajorUpgradeRolledBack || latest.Converted || !strings.Contains(latest.Error, "nothing was changed") {
		t.Fatalf("record = %+v", latest)
	}
	if strings.Contains(latest.Error, "bucket") {
		t.Fatalf("the cause leaked to the user: %q", latest.Error)
	}
	for _, call := range w.sequence() {
		if strings.HasPrefix(call, "convert") || call == "stop-replication" {
			t.Fatalf("ran %s after a failed backup", call)
		}
	}
	if inst := projectRow(t, w); inst.Status != string(domain.StatusActive) || inst.PostgresVersion != "16" {
		t.Fatalf("project = %s on %s", inst.Status, inst.PostgresVersion)
	}
}

func TestMajorUpgrade_RolledBackConversionRestartsReplicationOnTheOldMajor(t *testing.T) {
	w, svc := newUpgradeWorld(t, upgradeProject())
	w.outcome = UpgradeOutcome{RolledBack: true, Public: "the upgrade did not complete, so the database was put back on 16", Cause: errors.New("job failed")}
	if _, err := svc.Start(context.Background(), "proj-up", confirmed("17"), "user-1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	latest, _ := w.runs.LatestMajorUpgrade(context.Background(), "proj-up")
	if latest.Status != domain.MajorUpgradeRolledBack || latest.Error != w.outcome.Public {
		t.Fatalf("record = %+v", latest)
	}
	calls := strings.Join(w.sequence(), ",")
	if !strings.HasSuffix(calls, "convert:17,restart-replication") {
		t.Fatalf("calls = %s", calls)
	}
	if inst := projectRow(t, w); inst.Status != string(domain.StatusActive) || inst.PostgresVersion != "16" {
		t.Fatalf("project = %s on %s", inst.Status, inst.PostgresVersion)
	}
}

func TestMajorUpgrade_FailureThatCannotBePutBackLeavesTheProjectFailedWithTheBackup(t *testing.T) {
	w, svc := newUpgradeWorld(t, upgradeProject())
	w.outcome = UpgradeOutcome{Public: "the old version did not start again", Cause: errors.New("revert timed out")}
	w.probeErrs = []error{errors.New("connection refused")}
	if _, err := svc.Start(context.Background(), "proj-up", confirmed("17"), "user-1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	latest, _ := w.runs.LatestMajorUpgrade(context.Background(), "proj-up")
	if latest.Status != domain.MajorUpgradeFailed || !strings.Contains(latest.Error, "backup-16") {
		t.Fatalf("record = %+v", latest)
	}
	inst := projectRow(t, w)
	if inst.Status != string(domain.StageFailed) || !strings.Contains(inst.FailureReason, "backup-16") {
		t.Fatalf("project = %s %q", inst.Status, inst.FailureReason)
	}
}

func TestMajorUpgrade_FailureWhoseDatabaseStillAnswersStaysActive(t *testing.T) {
	w, svc := newUpgradeWorld(t, upgradeProject())
	w.outcome = UpgradeOutcome{Public: "the upgrade is taking longer than expected", Cause: errors.New("job still running")}
	if _, err := svc.Start(context.Background(), "proj-up", confirmed("17"), "user-1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if inst := projectRow(t, w); inst.Status != string(domain.StatusActive) {
		t.Fatalf("project = %s", inst.Status)
	}
}

func TestMajorUpgrade_ConvertedButStepsAfterFailIsCompletedWithWarnings(t *testing.T) {
	w, svc := newUpgradeWorld(t, upgradeProject())
	w.servingErr = errors.New("replica still cloning")
	w.failOnce = map[string]error{"restart-replication": errors.New("watcher refused")}
	w.backupErrs = []error{nil, errors.New("a"), errors.New("b"), errors.New("c")}
	if _, err := svc.Start(context.Background(), "proj-up", confirmed("17"), "user-1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	latest, _ := w.runs.LatestMajorUpgrade(context.Background(), "proj-up")
	if latest.Status != domain.MajorUpgradeCompleted || !latest.Converted {
		t.Fatalf("record = %+v", latest)
	}
	joined := strings.Join(latest.Warnings, " | ")
	for _, want := range []string{"not every copy", "realtime", "no backup on Postgres 17 yet"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings %q miss %q", joined, want)
		}
	}
	if strings.Contains(joined, "cloning") || strings.Contains(joined, "watcher refused") {
		t.Fatalf("a cause leaked into the warnings: %q", joined)
	}
	if inst := projectRow(t, w); inst.Status != string(domain.StatusActive) || inst.PostgresVersion != "17" {
		t.Fatalf("project = %s on %s", inst.Status, inst.PostgresVersion)
	}
}

func TestMajorUpgrade_RecordFailsToConvertIsAFailure(t *testing.T) {
	w, svc := newUpgradeWorld(t, upgradeProject())
	w.runs.markErr = errors.New("platform db down")
	if _, err := svc.Start(context.Background(), "proj-up", confirmed("17"), "user-1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	latest, _ := w.runs.LatestMajorUpgrade(context.Background(), "proj-up")
	if latest.Status != domain.MajorUpgradeFailed {
		t.Fatalf("record = %+v", latest)
	}
}

func TestMajorUpgrade_SecondStartWhileRunningIsRefused(t *testing.T) {
	w, svc := newUpgradeWorld(t, upgradeProject())
	if err := w.runs.StartMajorUpgrade(context.Background(), &domain.MajorUpgrade{ID: "other", ProjectID: "proj-up", FromVersion: "16", ToVersion: "17"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(context.Background(), "proj-up", confirmed("17"), "user-1"); !errors.Is(err, storage.ErrMajorUpgradeRunning) {
		t.Fatalf("Start = %v", err)
	}
}

func TestMajorUpgrade_RecoverPicksUpARunWhoseDriverDied(t *testing.T) {
	project := upgradeProject()
	project.Status = string(domain.StatusUpgrading)
	project.CurrentStep = domain.MajorUpgradeStepConverting
	w, svc := newUpgradeWorld(t, project)
	if err := w.runs.StartMajorUpgrade(context.Background(), &domain.MajorUpgrade{ID: "orphan", StartedAt: time.Now().Add(-time.Hour), ProjectID: "proj-up", FromVersion: "16", ToVersion: "17", SafetyBackupID: "backup-16"}); err != nil {
		t.Fatal(err)
	}
	w.recovered = UpgradeOutcome{Converted: true}
	svc.RecoverInterrupted(context.Background())
	latest, _ := w.runs.LatestMajorUpgrade(context.Background(), "proj-up")
	if latest.Status != domain.MajorUpgradeCompleted || !latest.Converted {
		t.Fatalf("record = %+v", latest)
	}
	if !strings.Contains(strings.Join(w.sequence(), ","), "recover:17") {
		t.Fatalf("calls = %v", w.sequence())
	}
	if inst := projectRow(t, w); inst.Status != string(domain.StatusActive) || inst.PostgresVersion != "17" {
		t.Fatalf("project = %s on %s", inst.Status, inst.PostgresVersion)
	}
}

func TestMajorUpgrade_RecoverSkipsARunSomeoneIsDriving(t *testing.T) {
	project := upgradeProject()
	project.Status = string(domain.StatusUpgrading)
	w, svc := newUpgradeWorld(t, project)
	if err := w.runs.StartMajorUpgrade(context.Background(), &domain.MajorUpgrade{ID: "live", StartedAt: time.Now().Add(-time.Hour), ProjectID: "proj-up", FromVersion: "16", ToVersion: "17"}); err != nil {
		t.Fatal(err)
	}
	w.holdErr = ErrProjectOperationRunning
	svc.RecoverInterrupted(context.Background())
	if latest, _ := w.runs.LatestMajorUpgrade(context.Background(), "proj-up"); latest.Status != domain.MajorUpgradeRunning {
		t.Fatalf("a driven run was touched: %+v", latest)
	}
}

func TestMajorUpgrade_RecoverOfAConvertedRunOnlyFinishes(t *testing.T) {
	project := upgradeProject()
	project.Status = string(domain.StatusUpgrading)
	w, svc := newUpgradeWorld(t, project)
	run := &domain.MajorUpgrade{ID: "half", StartedAt: time.Now().Add(-time.Hour), ProjectID: "proj-up", FromVersion: "16", ToVersion: "17"}
	if err := w.runs.StartMajorUpgrade(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := w.runs.MarkMajorUpgradeConverted(context.Background(), "half", "proj-up", "17", time.Now()); err != nil {
		t.Fatal(err)
	}
	svc.RecoverInterrupted(context.Background())
	calls := strings.Join(w.sequence(), ",")
	if strings.Contains(calls, "recover:") || !strings.Contains(calls, "wait-serving") {
		t.Fatalf("calls = %s", calls)
	}
	latest, _ := w.runs.LatestMajorUpgrade(context.Background(), "proj-up")
	if latest.Status != domain.MajorUpgradeCompleted {
		t.Fatalf("record = %+v", latest)
	}
}

func TestMajorUpgradeStatus_ReportsTargetsBlockersAndTheLatestRun(t *testing.T) {
	w, svc := newUpgradeWorld(t, upgradeProject())
	w.blockers = []string{"prepared transactions"}
	status, err := svc.Status(context.Background(), "proj-up")
	if err != nil {
		t.Fatal(err)
	}
	if !status.Supported || status.CurrentVersion != "16" || strings.Join(status.Targets, ",") != "17" ||
		len(status.Blockers) != 1 || status.Downtime == "" || status.Latest != nil {
		t.Fatalf("status = %+v", status)
	}
}

func TestMajorUpgradeStatus_ExplainsWhatIsNotSupported(t *testing.T) {
	for name, mutate := range map[string]func(*domain.DatabaseInstance){
		"mysql":      func(i *domain.DatabaseInstance) { i.DBType = domain.MySQL; i.MySQLVersion = "8.4.10" },
		"documentdb": func(i *domain.DatabaseInstance) { i.DocumentDB = true },
		"newest":     func(i *domain.DatabaseInstance) { i.PostgresVersion = "17" },
	} {
		t.Run(name, func(t *testing.T) {
			project := upgradeProject()
			mutate(project)
			w, svc := newUpgradeWorld(t, project)
			status, err := svc.Status(context.Background(), "proj-up")
			if err != nil {
				t.Fatal(err)
			}
			if status.Reason == "" || len(status.Targets) != 0 || len(status.Blockers) != 0 {
				t.Fatalf("status = %+v", status)
			}
			for _, call := range w.sequence() {
				if strings.HasPrefix(call, "preflight") {
					t.Fatal("an unsupported project was checked")
				}
			}
		})
	}
}

func TestMajorUpgradeStatus_ChecksOnlyAnActiveProject(t *testing.T) {
	project := upgradeProject()
	project.Status = string(domain.StatusPaused)
	w, svc := newUpgradeWorld(t, project)
	status, err := svc.Status(context.Background(), "proj-up")
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Blockers) != 1 || !strings.Contains(status.Blockers[0], "PAUSED") {
		t.Fatalf("status = %+v", status)
	}
	for _, call := range w.sequence() {
		if strings.HasPrefix(call, "preflight") {
			t.Fatal("a paused project was checked")
		}
	}
}

func TestMajorUpgradeStatus_ACheckThatCannotRunIsABlocker(t *testing.T) {
	w, svc := newUpgradeWorld(t, upgradeProject())
	w.preflightErr = errors.New("exec: connection refused 10.0.0.1")
	status, err := svc.Status(context.Background(), "proj-up")
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Blockers) != 1 || strings.Contains(status.Blockers[0], "10.0.0.1") {
		t.Fatalf("blockers = %v", status.Blockers)
	}
}

// Any member can read the status; the database checks behind it run at most
// once a minute per project. Starting an upgrade always checks afresh.
func TestMajorUpgradeStatus_ChecksTheDatabaseAtMostOnceAMinute(t *testing.T) {
	w, svc := newUpgradeWorld(t, upgradeProject())
	now := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	svc.cfg.Now = func() time.Time { return now }
	preflights := func() int {
		count := 0
		for _, call := range w.sequence() {
			if strings.HasPrefix(call, "preflight") {
				count++
			}
		}
		return count
	}
	for range 3 {
		if _, err := svc.Status(context.Background(), "proj-up"); err != nil {
			t.Fatal(err)
		}
	}
	if preflights() != 1 {
		t.Fatalf("checked %d times", preflights())
	}
	now = now.Add(time.Minute + time.Second)
	_, _ = svc.Status(context.Background(), "proj-up")
	if preflights() != 2 {
		t.Fatalf("a stale answer was served: %d checks", preflights())
	}
	if _, err := svc.Start(context.Background(), "proj-up", confirmed("17"), "u"); err != nil {
		t.Fatal(err)
	}
	if preflights() != 3 {
		t.Fatalf("a start used a remembered check: %d", preflights())
	}
}

// A run whose driver still marks it alive is left alone even when its lease
// is free: a lost lease must not start a second driver.
func TestMajorUpgrade_RecoverSkipsARunWithAFreshHeartbeat(t *testing.T) {
	project := upgradeProject()
	project.Status = string(domain.StatusUpgrading)
	w, svc := newUpgradeWorld(t, project)
	if err := w.runs.StartMajorUpgrade(context.Background(), &domain.MajorUpgrade{ID: "alive", ProjectID: "proj-up", FromVersion: "16", ToVersion: "17", StartedAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := w.runs.TouchMajorUpgrade(context.Background(), "alive", time.Now()); err != nil {
		t.Fatal(err)
	}
	svc.RecoverInterrupted(context.Background())
	if strings.Contains(strings.Join(w.sequence(), ","), "recover:") {
		t.Fatalf("a driven run was recovered: %v", w.sequence())
	}
}

// A finished run cannot be written again by a driver that outlived it.
func TestMajorUpgrade_AFinishedRunIsNotRewritten(t *testing.T) {
	_, svc := newUpgradeWorld(t, upgradeProject())
	run, err := svc.Start(context.Background(), "proj-up", confirmed("17"), "u")
	if err != nil {
		t.Fatal(err)
	}
	stale := *run
	stale.Status = domain.MajorUpgradeFailed
	if err := svc.cfg.Runs.UpdateMajorUpgrade(context.Background(), &stale); !errors.Is(err, storage.ErrMajorUpgradeNotRunning) {
		t.Fatalf("update of a finished run = %v", err)
	}
}

// A conversion that has not settled keeps the run open and the project
// upgrading, so recovery checks it again; nothing is marked failed.
func TestMajorUpgrade_AnUnsettledConversionStaysOpen(t *testing.T) {
	w, svc := newUpgradeWorld(t, upgradeProject())
	w.outcome = UpgradeOutcome{Unsettled: true, Public: "still converting"}
	if _, err := svc.Start(context.Background(), "proj-up", confirmed("17"), "u"); err != nil {
		t.Fatal(err)
	}
	latest, _ := w.runs.LatestMajorUpgrade(context.Background(), "proj-up")
	if latest.Status != domain.MajorUpgradeRunning || latest.Step != domain.MajorUpgradeStepWaiting {
		t.Fatalf("run = %+v", latest)
	}
	if inst := projectRow(t, w); inst.Status != string(domain.StatusUpgrading) {
		t.Fatalf("project = %s", inst.Status)
	}
}
