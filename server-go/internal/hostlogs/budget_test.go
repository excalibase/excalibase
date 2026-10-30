package hostlogs

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/enginestats"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// memBudgets stands in for the platform database's budget table.
type memBudgets struct {
	mu       sync.Mutex
	rows     map[budgetKey]dayUse
	reads    int
	readErr  error
	writeErr error
	pruned   string
}

func (m *memBudgets) HostLogBudgetUse(_ context.Context, projectID, day string) (int64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	if m.readErr != nil {
		return 0, false, m.readErr
	}
	use := m.rows[budgetKey{projectID, day}]
	return use.bytes, use.noticed, nil
}

func (m *memBudgets) AddHostLogBudgetUse(_ context.Context, projectID, day string, bytes int64, noticed bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.writeErr != nil {
		return m.writeErr
	}
	if m.rows == nil {
		m.rows = map[budgetKey]dayUse{}
	}
	use := m.rows[budgetKey{projectID, day}]
	use.bytes += bytes
	use.noticed = use.noticed || noticed
	m.rows[budgetKey{projectID, day}] = use
	return nil
}

func (m *memBudgets) PruneHostLogBudget(_ context.Context, before string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruned = before
	for key := range m.rows {
		if key.day < before {
			delete(m.rows, key)
		}
	}
	return nil
}

func (m *memBudgets) use(projectID string, at time.Time) dayUse {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rows[budgetKey{projectID, at.UTC().Format(time.DateOnly)}]
}

func TestBudgetResetsEachUTCDay(t *testing.T) {
	ctx := context.Background()
	budget := newBudget(nil)
	if keep, notice := budget.take(ctx, "p", 10, now, 8); !keep || notice {
		t.Error("first 8 bytes refused")
	}
	if keep, notice := budget.take(ctx, "p", 10, now, 8); keep || !notice {
		t.Error("over budget: want a notice")
	}
	if keep, notice := budget.take(ctx, "p", 10, now, 3); keep || notice {
		t.Error("the notice is given once a day")
	}
	if keep, _ := budget.take(ctx, "q", 10, now, 8); !keep {
		t.Error("budgets are per project")
	}
	if keep, _ := budget.take(ctx, "p", 10, now.Add(24*time.Hour), 8); !keep {
		t.Error("a new day starts a new budget")
	}
	// A late line of yesterday still counts against yesterday, not today.
	if keep, notice := budget.take(ctx, "p", 10, now, 3); keep || notice {
		t.Error("yesterday's budget was forgotten by today's line")
	}
}

// EXC-678: the day's use is in the platform database, so a restart resumes
// the count instead of granting a second budget.
func TestBudgetSurvivesARestart(t *testing.T) {
	ctx := context.Background()
	store := &memBudgets{}
	first := newBudget(store)
	if keep, _ := first.take(ctx, "p", 10, now, 8); !keep {
		t.Fatal("first 8 bytes refused")
	}
	if err := first.record(ctx, "p", map[string]*charge{now.Format(time.DateOnly): {bytes: 8}}); err != nil {
		t.Fatal(err)
	}
	restarted := newBudget(store)
	if keep, notice := restarted.take(ctx, "p", 10, now, 8); keep || !notice {
		t.Error("a restart granted the day's budget again")
	}
	if err := restarted.record(ctx, "p", map[string]*charge{now.Format(time.DateOnly): {noticed: true}}); err != nil {
		t.Fatal(err)
	}
	// The notice is persisted too: one per day, restarts included.
	again := newBudget(store)
	if keep, notice := again.take(ctx, "p", 10, now, 8); keep || notice {
		t.Error("a second notice after a restart")
	}
	// Loaded once per project and day, not per line.
	reads := store.reads
	again.take(ctx, "p", 10, now, 1)
	again.take(ctx, "p", 10, now, 1)
	if store.reads != reads {
		t.Errorf("read the store %d more times", store.reads-reads)
	}
}

// The database down: the budget still holds for this run, from zero.
func TestBudgetWithTheStoreDownStillCounts(t *testing.T) {
	ctx := context.Background()
	store := &memBudgets{readErr: errors.New("db down"), writeErr: errors.New("db down")}
	budget := newBudget(store)
	if keep, _ := budget.take(ctx, "p", 10, now, 8); !keep {
		t.Fatal("refused with the store down")
	}
	if keep, _ := budget.take(ctx, "p", 10, now, 8); keep {
		t.Error("the in-memory count is lost with the store down")
	}
	if err := budget.record(ctx, "p", map[string]*charge{now.Format(time.DateOnly): {bytes: 8}}); err == nil {
		t.Error("a failed write reported success")
	}
}

func TestBudgetForgetsOldDaysInMemory(t *testing.T) {
	ctx := context.Background()
	budget := newBudget(nil)
	budget.take(ctx, "p", 10, now.Add(-72*time.Hour), 1)
	budget.take(ctx, "p", 10, now, 1)
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if len(budget.used) != 1 {
		t.Errorf("kept %d days", len(budget.used))
	}
}

// Each project's budget is its plan's; a plan not named takes the default.
func TestTargetsCarryTheirPlansDailyBudget(t *testing.T) {
	engine := &fakeEngine{containers: []enginestats.Container{
		{ID: "db-a", Name: provisioner.DatabaseContainerName("proj-a"), State: "running"},
		{ID: "db-b", Name: provisioner.DatabaseContainerName("proj-b"), State: "running"},
		{ID: "db-c", Name: provisioner.DatabaseContainerName("proj-c"), State: "running"},
	}}
	shipper := newTestShipper(engine, &fakeCursors{}, &fakePusher{}, features.NewStatic(),
		dockerProject("proj-a", domain.Free, "db-a"), dockerProject("proj-b", domain.Standard, "db-b"),
		dockerProject("proj-c", domain.Enterprise, "db-c"))
	shipper.deps.DailyBytesByPlan = map[domain.TierType]int64{domain.Free: 64 << 20, domain.Standard: 256 << 20}
	targets, _, err := shipper.targets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, target := range targets {
		got[target.Labels.ProjectID] = target.DailyBytes
	}
	if got["proj-a"] != 64<<20 || got["proj-b"] != 256<<20 || got["proj-c"] != 1<<20 {
		t.Errorf("budgets = %v", got)
	}
}

// What a batch keeps is written to the budget table once Loki stores it.
func TestShipStreamPersistsTheDaysUse(t *testing.T) {
	engine := &fakeEngine{logs: map[string][]enginestats.LogLine{"db-a": {line(1, "12345"), line(2, "678"), line(3, "this one is over")}}}
	store := &memBudgets{}
	shipper := newTestShipper(engine, &fakeCursors{}, &fakePusher{}, features.NewStatic())
	shipper.budget = newBudget(store)
	target := Target{ID: "db-a", Labels: Labels{ProjectID: "proj-a", Component: "database", Container: "c"}, Retention: time.Hour, DailyBytes: 10}
	if _, err := shipper.shipStream(context.Background(), target, 0); err != nil {
		t.Fatal(err)
	}
	if use := store.use("proj-a", now); use.bytes != 8 || !use.noticed {
		t.Errorf("stored use = %+v", use)
	}
}

// The notice names the project's own budget.
func TestBudgetNoticeNamesThePlansFigure(t *testing.T) {
	var lines []enginestats.LogLine
	for i := 0; i < 70; i++ {
		lines = append(lines, line(i-100, strings.Repeat("x", 16<<10)))
	}
	engine := &fakeEngine{logs: map[string][]enginestats.LogLine{"db-a": lines}}
	pusher := &fakePusher{}
	shipper := newTestShipper(engine, &fakeCursors{}, pusher, features.NewStatic())
	target := Target{ID: "db-a", Labels: Labels{ProjectID: "proj-a", Component: "database", Container: "c"}, Retention: time.Hour, DailyBytes: 1 << 20}
	if _, err := shipper.shipStream(context.Background(), target, 0); err != nil {
		t.Fatal(err)
	}
	pushed := pusher.lines()
	if last := pushed[len(pushed)-1]; len(pushed) != 65 || !strings.Contains(last, "(1 MiB)") {
		t.Errorf("pushed %d lines, last %.100q", len(pushed), last)
	}
}

func TestScanPrunesOldBudgetDays(t *testing.T) {
	engine := &fakeEngine{containers: []enginestats.Container{{ID: "db-a", Name: provisioner.DatabaseContainerName("proj-a"), State: "exited"}}}
	store := &memBudgets{}
	shipper := newTestShipper(engine, &fakeCursors{}, &fakePusher{}, features.NewStatic(), dockerProject("proj-a", domain.Free, "db-a"))
	shipper.deps.Budgets = store
	if err := shipper.Scan(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if want := now.Add(-48 * time.Hour).Format(time.DateOnly); store.pruned != want {
		t.Errorf("pruned before %q, want %q", store.pruned, want)
	}
}
