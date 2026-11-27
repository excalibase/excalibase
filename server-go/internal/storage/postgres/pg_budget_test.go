//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/planbudget"
)

// A Free database is 0.5 vCPU, 512 MiB and a 5 GiB disk (the seeded plan row).
const (
	freeDatabaseCPU  = 500
	freeDatabaseDisk = int64(5) << 30
)

func gateOf(cpuMilli, memoryMiB, diskGiB, services int, on bool) *planbudget.Gate {
	numbers := config.PlanNumbers{CPUMilli: cpuMilli, MemoryMiB: memoryMiB, DiskGiB: diskGiB, MaxProjects: 50, MaxServices: services}
	return planbudget.NewGate(func(context.Context) bool { return on }, config.PlanBudgetConfig{Free: numbers, Standard: numbers})
}

// budgetOrg is a store whose gate allows `cpuMilli` of CPU (and plenty of the rest), and a free org on it.
func budgetOrg(t *testing.T, cpuMilli int) (*Store, string) {
	t.Helper()
	store := testStore(t)
	store.SetPlanBudget(gateOf(cpuMilli, 64*1024, 500, 50, true))
	return store, creditPerson(t, store, "bo")
}

func orgOf(t *testing.T, store *Store, person string) string {
	t.Helper()
	return creditPerson(t, store, person)
}

func isRefusal(err error) bool {
	var refusal *planbudget.Refusal
	return errors.As(err, &refusal)
}

// race runs the steps at the same moment and returns what each answered.
func race(steps []func() error) []error {
	results := make([]error, len(steps))
	var start, done sync.WaitGroup
	start.Add(1)
	for i, step := range steps {
		done.Add(1)
		go func(i int, step func() error) {
			defer done.Done()
			start.Wait()
			results[i] = step()
		}(i, step)
	}
	start.Done()
	done.Wait()
	return results
}

// exactlyOne checks that one step committed and every other was refused for the budget.
func exactlyOne(t *testing.T, results []error) {
	t.Helper()
	admitted := 0
	for i, err := range results {
		switch {
		case err == nil:
			admitted++
		case isRefusal(err):
		default:
			t.Fatalf("step %d: unexpected error %v", i, err)
		}
	}
	if admitted != 1 {
		t.Fatalf("%d steps were admitted, want exactly 1: %v", admitted, results)
	}
}

func newDatabase(t *testing.T, store *Store, project, org string) {
	t.Helper()
	if err := store.Create(instanceRow(project, org)); err != nil {
		t.Fatalf("seed %s: %v", project, err)
	}
}

func TestCreateRefusesTheStepThatPassesTheBudgetAndNamesWhatIsLeft(t *testing.T) {
	store, org := budgetOrg(t, 1000)
	newDatabase(t, store, "proj-b-one", org)
	newDatabase(t, store, "proj-b-two", org)
	err := store.Create(instanceRow("proj-b-three", org))
	var refusal *planbudget.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("a third 0.5 vCPU database on 1 vCPU: %v", err)
	}
	if refusal.Dimension != "CPU" || refusal.Left.CPUMilli != 0 || refusal.Need.CPUMilli != freeDatabaseCPU {
		t.Fatalf("%+v", refusal)
	}
	if got, _ := store.FindByProjectID("proj-b-three"); got != nil {
		t.Fatal("a refused create must leave no row")
	}
}

func TestConcurrentCreatesOfADatabaseFillTheBudgetOnce(t *testing.T) {
	store, org := budgetOrg(t, 1000)
	newDatabase(t, store, "proj-c-seed", org)
	steps := make([]func() error, 6)
	for i := range steps {
		project := fmt.Sprintf("proj-c-racer%d", i)
		steps[i] = func() error { return store.CreateWithinOrgLimit(instanceRow(project, org), 0) }
	}
	exactlyOne(t, race(steps))
}

func TestConcurrentProjectCreatesWithALimitFillTheBudgetOnce(t *testing.T) {
	store, org := budgetOrg(t, 1000)
	newDatabase(t, store, "proj-l-seed", org)
	steps := make([]func() error, 6)
	for i := range steps {
		project := fmt.Sprintf("proj-l-racer%d", i)
		steps[i] = func() error { return store.CreateWithinOrgLimit(instanceRow(project, org), 10) }
	}
	exactlyOne(t, race(steps))
}

// A restore re-enters a project that was in its grace period; its database comes back.
func TestConcurrentRestoresFillTheBudgetOnce(t *testing.T) {
	store, org := budgetOrg(t, 1000)
	newDatabase(t, store, "proj-r-seed", org)
	steps := make([]func() error, 4)
	for i := range steps {
		project := fmt.Sprintf("proj-r-racer%d", i)
		if err := store.Create(pendingDeletionRow(project, org)); err != nil {
			t.Fatalf("seed %s: %v", project, err)
		}
		steps[i] = func() error {
			back := instanceRow(project, org)
			return store.UpdateIfStatusWithinOrgLimit(back, string(domain.StatusPendingDeletion), 10)
		}
	}
	exactlyOne(t, race(steps))
}

func TestConcurrentAddsOfADatabaseFillTheBudgetOnce(t *testing.T) {
	store, org := budgetOrg(t, 1000)
	newDatabase(t, store, "proj-a-seed", org)
	steps := make([]func() error, 4)
	for i := range steps {
		project := fmt.Sprintf("proj-a-racer%d", i)
		empty := instanceRow(project, org)
		empty.NoDatabase = true
		if err := store.Create(empty); err != nil {
			t.Fatalf("seed %s: %v", project, err)
		}
		steps[i] = func() error {
			adding := instanceRow(project, org)
			return store.RecordDatabaseChoices(adding, string(domain.StatusActive))
		}
	}
	exactlyOne(t, race(steps))
}

func TestAFailedAddGivesItsReservationBack(t *testing.T) {
	store, org := budgetOrg(t, 500)
	empty := instanceRow("proj-f-empty", org)
	empty.NoDatabase = true
	if err := store.Create(empty); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDatabaseChoices(instanceRow("proj-f-empty", org), string(domain.StatusActive)); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(instanceRow("proj-f-other", org)); !isRefusal(err) {
		t.Fatalf("the add holds the budget while it runs: %v", err)
	}
	failed := instanceRow("proj-f-empty", org)
	failed.NoDatabase = true
	if err := store.Update(failed); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(instanceRow("proj-f-other", org)); err != nil {
		t.Fatalf("a failed add must free what it held: %v", err)
	}
}

func TestConcurrentResizesFillTheBudgetOnce(t *testing.T) {
	store, org := budgetOrg(t, 1200)
	ctx := context.Background()
	steps := make([]func() error, 2)
	grown := planbudget.Reservation{PerCopy: planbudget.Size{CPUMilli: 650, MemoryBytes: 512 << 20, DiskBytes: freeDatabaseDisk}, Copies: 1}
	for i := range steps {
		project := fmt.Sprintf("proj-z-racer%d", i)
		newDatabase(t, store, project, org)
		steps[i] = func() error { _, err := store.ReserveDatabaseSize(ctx, project, grown); return err }
	}
	exactlyOne(t, race(steps))
}

func TestConcurrentDiskGrowthFillsTheBudgetOnce(t *testing.T) {
	store := testStore(t)
	store.SetPlanBudget(gateOf(64*1000, 64*1024, 20, 50, true))
	org := orgOf(t, store, "diskowner")
	steps := make([]func() error, 2)
	for i := range steps {
		project := fmt.Sprintf("proj-d-racer%d", i)
		newDatabase(t, store, project, org)
		steps[i] = func() error { return store.UpdateStorageSizeIfStatus(project, "14Gi", "ACTIVE") }
	}
	exactlyOne(t, race(steps))
}

func TestConcurrentResumesFillTheBudgetOnce(t *testing.T) {
	store, org := budgetOrg(t, 1000)
	newDatabase(t, store, "proj-u-live", org)
	steps := make([]func() error, 4)
	for i := range steps {
		project := fmt.Sprintf("proj-u-racer%d", i)
		sleeper := instanceRow(project, org)
		sleeper.Status = string(domain.StatusPaused)
		if err := store.Create(sleeper); err != nil {
			t.Fatalf("seed %s: %v", project, err)
		}
		steps[i] = func() error {
			waking := instanceRow(project, org)
			waking.Status = string(domain.StatusResuming)
			return store.UpdateIfStatus(waking, string(domain.StatusPaused))
		}
	}
	exactlyOne(t, race(steps))
}

// A resume the budget refused leaves the project paused, and the retry after
// room is made goes through the same door.
func TestAResumeThatWasRefusedCanBeRetriedOnceThereIsRoom(t *testing.T) {
	store, org := budgetOrg(t, 500)
	newDatabase(t, store, "proj-t-live", org)
	sleeper := instanceRow("proj-t-sleep", org)
	sleeper.Status = string(domain.StatusPaused)
	if err := store.Create(sleeper); err != nil {
		t.Fatal(err)
	}
	resume := func() error {
		waking := instanceRow("proj-t-sleep", org)
		waking.Status = string(domain.StatusResuming)
		return store.UpdateIfStatus(waking, string(domain.StatusPaused))
	}
	if err := resume(); !isRefusal(err) {
		t.Fatalf("no room: %v", err)
	}
	if got, _ := store.FindByProjectID("proj-t-sleep"); got.Status != string(domain.StatusPaused) {
		t.Fatalf("a refused resume must stay paused, is %s", got.Status)
	}
	paused := instanceRow("proj-t-live", org)
	paused.Status = string(domain.StatusPaused)
	if err := store.Update(paused); err != nil {
		t.Fatalf("pausing frees room and always passes: %v", err)
	}
	if err := resume(); err != nil {
		t.Fatalf("the retry with room: %v", err)
	}
}

func TestConcurrentFirstFunctionsFillTheBudgetOnce(t *testing.T) {
	store := testStore(t)
	store.SetPlanBudget(gateOf(freeDatabaseCPU+200, 64*1024, 500, 50, true))
	org := orgOf(t, store, "fnowner")
	steps := make([]func() error, 4)
	for i := range steps {
		project := fmt.Sprintf("proj-n-racer%d", i)
		empty := instanceRow(project, org)
		empty.NoDatabase = true
		if err := store.Create(empty); err != nil {
			t.Fatal(err)
		}
		steps[i] = func() error { return store.EnsureFunctionsRuntime(context.Background(), project) }
	}
	newDatabase(t, store, "proj-n-seed", org)
	exactlyOne(t, race(steps))
	if err := store.EnsureFunctionsRuntime(context.Background(), "proj-n-seed"); !isRefusal(err) {
		t.Fatalf("the seed's own runtime needs room too: %v", err)
	}
}

func TestShrinkDeletePauseAndResizeDownAlwaysPassForAnOrgOverItsBudget(t *testing.T) {
	store, org := budgetOrg(t, 100000)
	for _, project := range []string{"proj-o-a", "proj-o-b", "proj-o-c"} {
		newDatabase(t, store, project, org)
	}
	store.SetPlanBudget(gateOf(100, 64*1024, 1, 50, true))
	ctx := context.Background()
	if err := store.Create(instanceRow("proj-o-d", org)); !isRefusal(err) {
		t.Fatalf("over budget refuses growth: %v", err)
	}
	if _, err := store.ReserveDatabaseSize(ctx, "proj-o-a", planbudget.Reservation{PerCopy: planbudget.Size{CPUMilli: 100, MemoryBytes: 1 << 20, DiskBytes: 1 << 30}, Copies: 1}); err != nil {
		t.Fatalf("resize down: %v", err)
	}
	if err := store.UpdateStorageSizeIfStatus("proj-o-b", "1Gi", "ACTIVE"); err != nil {
		t.Fatalf("shrink the disk: %v", err)
	}
	paused := instanceRow("proj-o-b", org)
	paused.Status = string(domain.StatusPaused)
	paused.StorageSize = "1Gi"
	if err := store.Update(paused); err != nil {
		t.Fatalf("pause: %v", err)
	}
	deleting := instanceRow("proj-o-c", org)
	deleting.Status = string(domain.StatusDeleting)
	if err := store.Update(deleting); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := store.Delete("proj-o-a"); err != nil {
		t.Fatalf("remove: %v", err)
	}
}

func TestAdmissionIsOffWhileTheFlagIsOff(t *testing.T) {
	store := testStore(t)
	store.SetPlanBudget(gateOf(1, 1, 1, 1, false))
	org := orgOf(t, store, "offowner")
	for i := 0; i < 4; i++ {
		newDatabase(t, store, fmt.Sprintf("proj-off-%d", i), org)
	}
	if err := store.UpdateStorageSizeIfStatus("proj-off-0", "400Gi", "ACTIVE"); err != nil {
		t.Fatalf("a growth with the flag off: %v", err)
	}
	if err := store.EnsureFunctionsRuntime(context.Background(), "proj-off-0"); err != nil {
		t.Fatal(err)
	}
}

// With the budget on and credits off, every shape a Free org runs today fits the default plan.
func TestEveryCurrentFreeShapeIsAdmittedUnderTheDefaultPlan(t *testing.T) {
	store := testStore(t)
	free := config.Load().PlanBudget
	store.SetPlanBudget(planbudget.NewGate(func(context.Context) bool { return true }, free))
	org := orgOf(t, store, "shapeowner")
	newDatabase(t, store, "proj-s-db", org)
	if err := store.UpdateStorageSizeIfStatus("proj-s-db", "10Gi", "ACTIVE"); err != nil {
		t.Fatalf("a bigger disk: %v", err)
	}
	if err := store.EnsureFunctionsRuntime(context.Background(), "proj-s-db"); err != nil {
		t.Fatalf("functions: %v", err)
	}
}

func TestTheBackfillRecordsWhatADatabaseHoldsAndIsIdempotent(t *testing.T) {
	store := testStore(t)
	org := orgOf(t, store, "backowner")
	newDatabase(t, store, "proj-k-one", org)
	if _, err := store.db.Exec(`UPDATE database_instances SET reserved_copies = 0, reserved_cpu_milli = 0, reserved_memory_bytes = 0, reserved_disk_bytes = 0`); err != nil {
		t.Fatal(err)
	}
	n, err := store.BackfillReservations(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	var cpu, copies int64
	if err := store.db.QueryRow(`SELECT reserved_cpu_milli, reserved_copies FROM database_instances WHERE project_id = 'proj-k-one'`).Scan(&cpu, &copies); err != nil {
		t.Fatal(err)
	}
	if cpu != freeDatabaseCPU || copies != 1 {
		t.Fatalf("cpu %d copies %d", cpu, copies)
	}
	if again, _ := store.BackfillReservations(context.Background()); again != 0 {
		t.Fatalf("a second run found %d", again)
	}
}

func TestAResizeReturnsWhatTheDatabaseHeldSoItCanBePutBack(t *testing.T) {
	store, org := budgetOrg(t, 1000)
	newDatabase(t, store, "proj-v-one", org)
	ctx := context.Background()
	bigger := planbudget.Reservation{PerCopy: planbudget.Size{CPUMilli: 800, MemoryBytes: 1 << 30, DiskBytes: freeDatabaseDisk}, Copies: 1}
	previous, err := store.ReserveDatabaseSize(ctx, "proj-v-one", bigger)
	if err != nil || previous.PerCopy.CPUMilli != freeDatabaseCPU || previous.Copies != 1 {
		t.Fatalf("%+v %v", previous, err)
	}
	if _, err := store.ReserveDatabaseSize(ctx, "proj-v-one", previous); err != nil {
		t.Fatalf("putting it back is a resize down: %v", err)
	}
	if err := store.Create(instanceRow("proj-v-two", org)); err != nil {
		t.Fatalf("the room came back: %v", err)
	}
}

// Asking for a runtime a project already holds costs nothing, even with no room left.
func TestAFunctionsRuntimeAlreadyHeldIsNotChargedAgain(t *testing.T) {
	store := testStore(t)
	store.SetPlanBudget(gateOf(freeDatabaseCPU+200, 64*1024, 500, 50, true))
	org := orgOf(t, store, "fnagain")
	newDatabase(t, store, "proj-g-one", org)
	ctx := context.Background()
	if err := store.EnsureFunctionsRuntime(ctx, "proj-g-one"); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureFunctionsRuntime(ctx, "proj-g-one"); err != nil {
		t.Fatalf("the second call must be free: %v", err)
	}
}

// A step that only shrinks never sums the org's usage, so a record the budget cannot read does not stop it.
func TestAShrinkNeverReadsTheOrgsUsage(t *testing.T) {
	store, org := budgetOrg(t, 100000)
	newDatabase(t, store, "proj-h-one", org)
	if _, err := store.db.Exec(`UPDATE database_instances SET storage_size = '9Gi' WHERE project_id = 'proj-h-one'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO apps (project_id, id, name, status, version, doc) VALUES ('proj-h-one', 'broken', 'broken', 'ACTIVE', 1, '{"tier":"NOPE","replicas":1,"status":"ACTIVE"}')`); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateStorageSizeIfStatus("proj-h-one", "6Gi", "ACTIVE"); err != nil {
		t.Fatalf("a shrink must not depend on summing the org: %v", err)
	}
	if err := store.UpdateStorageSizeIfStatus("proj-h-one", "10Gi", "ACTIVE"); err == nil {
		t.Fatal("growth does sum the org, and an unreadable record is an error, never zero")
	}
}

// Every growth step waits for the org lock, so two of them can never sum the same free room.
func TestEveryGrowthStepWaitsForTheOrgLock(t *testing.T) {
	store, org := budgetOrg(t, 100000)
	newDatabase(t, store, "proj-w-live", org)
	sleeper := instanceRow("proj-w-sleep", org)
	sleeper.Status = string(domain.StatusPaused)
	if err := store.Create(sleeper); err != nil {
		t.Fatal(err)
	}
	steps := map[string]func() error{
		"create":    func() error { return store.Create(instanceRow("proj-w-new", org)) },
		"disk":      func() error { return store.UpdateStorageSizeIfStatus("proj-w-live", "6Gi", "ACTIVE") },
		"functions": func() error { return store.EnsureFunctionsRuntime(context.Background(), "proj-w-live") },
		"resize": func() error {
			_, err := store.ReserveDatabaseSize(context.Background(), "proj-w-live", planbudget.Reservation{PerCopy: planbudget.Size{CPUMilli: 600, MemoryBytes: 512 << 20, DiskBytes: freeDatabaseDisk}, Copies: 1})
			return err
		},
		"resume": func() error {
			waking := instanceRow("proj-w-sleep", org)
			waking.Status = string(domain.StatusResuming)
			return store.UpdateIfStatus(waking, string(domain.StatusPaused))
		},
	}
	for name, step := range steps {
		holder, err := store.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := holder.Exec(`SELECT pg_advisory_xact_lock($1, hashtext($2))`, orgProjectLimitLockSpace, org); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- step() }()
		select {
		case err := <-done:
			t.Fatalf("%s did not wait for the org lock: %v", name, err)
		case <-time.After(300 * time.Millisecond):
		}
		if err := holder.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatalf("%s after the lock was released: %v", name, err)
		}
	}
}
