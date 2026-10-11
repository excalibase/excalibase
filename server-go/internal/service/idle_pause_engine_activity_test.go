package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

type fakeEngineActivity struct {
	mu     sync.Mutex
	work   map[string]int64
	err    error
	probed []string
}

func (f *fakeEngineActivity) ClientWork(_ context.Context, inst *domain.DatabaseInstance) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probed = append(f.probed, inst.ProjectID)
	return f.work[inst.ProjectID], f.err
}

type fakeWorkCounters struct {
	mu     sync.Mutex
	counts map[string]int64
	err    error
}

func (f *fakeWorkCounters) DatabaseWork(_ context.Context, projectID string) (int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, false, f.err
	}
	work, known := f.counts[projectID]
	return work, known, nil
}

func (f *fakeWorkCounters) SaveDatabaseWork(_ context.Context, projectID string, work int64, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.counts[projectID] = work
	return nil
}

type engineActivityFixture struct {
	*idleFixture
	engine   *fakeEngineActivity
	counters *fakeWorkCounters
}

func newEngineActivityFixture(t *testing.T) *engineActivityFixture {
	t.Helper()
	f := &engineActivityFixture{
		idleFixture: newIdleFixture(t),
		engine:      &fakeEngineActivity{work: map[string]int64{}},
		counters:    &fakeWorkCounters{counts: map[string]int64{}},
	}
	f.scheduler.SetEngineActivity(domain.MySQL, f.engine, f.counters)
	return f
}

func (f *engineActivityFixture) mysqlProject(t *testing.T, id string, age time.Duration) {
	t.Helper()
	f.project(t, id, domain.Free, age)
	inst, _ := f.instances.FindByProjectID(id)
	inst.DBType = domain.MySQL
	if err := f.instances.Update(inst); err != nil {
		t.Fatal(err)
	}
}

// EXC-588: a MySQL project whose clients did nothing since the last sweep is
// idle like any other, and is paused through the same path.
func TestIdlePause_PausesMySQLProjectWithNoClientWork(t *testing.T) {
	f := newEngineActivityFixture(t)
	f.mysqlProject(t, "my1", 30*day)
	f.engine.work["my1"] = 120
	f.counters.counts["my1"] = 120

	report := f.run(t)
	if len(report.Paused) != 1 || report.Paused[0] != "my1" || f.pauser.reasons[0] != domain.PauseReasonIdle {
		t.Fatalf("an idle MySQL project was not paused: %+v", report)
	}
}

// Statements or connections from its clients since the last sweep are
// activity: the project is seen now, by the database.
func TestIdlePause_MySQLClientWorkKeepsProjectActive(t *testing.T) {
	f := newEngineActivityFixture(t)
	f.mysqlProject(t, "my1", 30*day)
	f.engine.work["my1"] = 121
	f.counters.counts["my1"] = 120

	report := f.run(t)
	if len(report.Paused)+len(report.Warned) != 0 {
		t.Fatalf("a MySQL project in use was treated as idle: %+v", report)
	}
	row, ok, _ := f.activity.GetProjectActivity(context.Background(), "my1")
	if !ok || !row.LastSeenAt.Equal(f.clock.Now()) || row.LastSeenSource != string(domain.ActivitySourceDatabase) {
		t.Errorf("activity row %+v (seen %v)", row, ok)
	}
	if f.counters.counts["my1"] != 121 {
		t.Errorf("counter not moved on: %d", f.counters.counts["my1"])
	}
}

// A counter that went down means mysqld restarted; what it did before is
// unknown, so it counts as activity rather than idleness.
func TestIdlePause_MySQLRestartCountsAsActivity(t *testing.T) {
	f := newEngineActivityFixture(t)
	f.mysqlProject(t, "my1", 30*day)
	f.engine.work["my1"] = 3
	f.counters.counts["my1"] = 500
	if report := f.run(t); len(report.Paused)+len(report.Warned) != 0 {
		t.Fatalf("a restarted MySQL project was paused: %+v", report)
	}
}

// With no earlier reading there is nothing to compare: the first reading is
// a baseline, taken as activity so a platform restart can never pause a
// project that was in use.
func TestIdlePause_FirstMySQLReadingIsABaseline(t *testing.T) {
	f := newEngineActivityFixture(t)
	f.mysqlProject(t, "my1", 30*day)
	f.engine.work["my1"] = 120

	if report := f.run(t); len(report.Paused)+len(report.Warned) != 0 {
		t.Fatalf("paused on its first reading: %+v", report)
	}
	f.clock.Advance(7*day + time.Hour)
	if report := f.run(t); len(report.Paused) != 1 {
		t.Fatalf("not paused after a week with no client work: %+v", report)
	}
}

// No reading, no verdict: a project whose database cannot be asked is
// neither warned nor paused.
func TestIdlePause_UnreadableMySQLProjectIsLeftAlone(t *testing.T) {
	for name, setup := range map[string]func(*engineActivityFixture){
		"database":    func(f *engineActivityFixture) { f.engine.err = errors.New("pod not ready") },
		"counter row": func(f *engineActivityFixture) { f.counters.err = errors.New("platform db down") },
	} {
		t.Run(name, func(t *testing.T) {
			f := newEngineActivityFixture(t)
			f.mysqlProject(t, "my1", 30*day)
			f.counters.counts["my1"] = 0
			setup(f)
			if report := f.run(t); len(report.Paused)+len(report.Warned) != 0 || f.pauser.count() != 0 {
				t.Fatalf("paused without a reading: %+v", report)
			}
		})
	}
}

// Only running projects are asked: a paused database has no clients.
func TestIdlePause_OnlyActiveMySQLProjectsAreRead(t *testing.T) {
	f := newEngineActivityFixture(t)
	f.mysqlProject(t, "my1", 30*day)
	inst, _ := f.instances.FindByProjectID("my1")
	inst.Status = string(domain.StatusPaused)
	_ = f.instances.Update(inst)
	f.run(t)
	if len(f.engine.probed) != 0 {
		t.Errorf("a paused project was read: %v", f.engine.probed)
	}
}

// Postgres projects keep their platform activity signal.
func TestIdlePause_EngineActivityIsPerEngine(t *testing.T) {
	f := newEngineActivityFixture(t)
	f.project(t, "pg1", domain.Free, 30*day)
	if report := f.run(t); len(report.Paused) != 1 || len(f.engine.probed) != 0 {
		t.Fatalf("report %+v probed %v", report, f.engine.probed)
	}
}

// The MySQL reader asks the project's own cluster, by its namespace.
func TestMySQLEngineActivityReadsTheProjectCluster(t *testing.T) {
	lab := newMySQLAdapterLab(t)
	ctx := context.Background()
	if _, err := lab.mysql.ProvisionWithRollback(ctx, domain.ProvisioningRequest{
		ProjectName: mysqlSourceProject, OrgID: "org1", DBType: domain.MySQL, MySQLVersion: "8.4.9",
	}, config.TierConfig{Instances: 1, StorageSize: "5Gi", CPU: "0.5", Memory: "512Mi"}, provisioner.NewProvisionContext(nil, nil)); err != nil {
		t.Fatalf("provision: %v", err)
	}
	lab.mock.WildcardExecOutput = "17\n"
	work, err := NewMySQLEngineActivity(lab.mysql).ClientWork(ctx, lab.source)
	if err != nil || work != 17 {
		t.Fatalf("work %d %v", work, err)
	}
}

// Work that could not be recorded as activity is not consumed: the next sweep
// still sees it.
func TestIdlePause_UnrecordedMySQLWorkIsReadAgain(t *testing.T) {
	f := newEngineActivityFixture(t)
	f.mysqlProject(t, "my1", 30*day)
	f.engine.work["my1"] = 121
	f.counters.counts["my1"] = 120
	f.activity.err = errors.New("platform db down")
	if report := f.run(t); len(report.Paused) != 0 {
		t.Fatalf("paused: %+v", report)
	}
	if f.counters.counts["my1"] != 120 {
		t.Fatalf("the reading was consumed: %d", f.counters.counts["my1"])
	}
	f.activity.err = nil
	if report := f.run(t); len(report.Paused)+len(report.Warned) != 0 {
		t.Fatalf("the unrecorded work was lost: %+v", report)
	}
}
