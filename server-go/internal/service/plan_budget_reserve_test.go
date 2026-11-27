package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/planbudget"
)

type recordingReserver struct {
	asked    []planbudget.Reservation
	restored []planbudget.Reservation
	inactive bool
	held     planbudget.Reservation
	err      error
}

func (r *recordingReserver) ReserveDatabaseSize(_ context.Context, _ string, next planbudget.Reservation) (planbudget.Reservation, error) {
	r.asked = append(r.asked, next)
	if r.err != nil {
		return planbudget.Reservation{}, r.err
	}
	previous := r.held
	r.held = next
	return previous, nil
}

func (r *recordingReserver) RestoreDatabaseSize(_ context.Context, _ string, previous planbudget.Reservation) error {
	r.restored = append(r.restored, previous)
	r.held = previous
	return nil
}

func (r *recordingReserver) BudgetActive(context.Context) bool { return !r.inactive }

var standardPlan = config.TierConfig{CPU: "2", Memory: "4Gi", Instances: 3, StorageSize: "50Gi"}

func TestAResizeReservesThePlansSizeTimesItsCopiesBeforeAnythingMoves(t *testing.T) {
	reserver := &recordingReserver{held: planbudget.Reservation{PerCopy: planbudget.Size{CPUMilli: 500}, Copies: 1}}
	svc := &ProvisioningService{reserver: reserver}
	putBack, err := svc.reserveResize(context.Background(), &domain.DatabaseInstance{ProjectID: "p"}, standardPlan, "60Gi")
	if err != nil {
		t.Fatal(err)
	}
	want := planbudget.Reservation{PerCopy: planbudget.Size{CPUMilli: 2000, MemoryBytes: 4 << 30, DiskBytes: 60 << 30}, Copies: 3}
	if reserver.asked[0] != want {
		t.Fatalf("%+v want %+v", reserver.asked[0], want)
	}
	putBack()
	if len(reserver.asked) != 1 {
		t.Fatalf("putting the old size back must not be admitted again: %+v", reserver.asked)
	}
	if len(reserver.restored) != 1 || reserver.restored[0].PerCopy.CPUMilli != 500 || reserver.restored[0].Copies != 1 {
		t.Fatalf("a failed change puts the old size back: %+v", reserver.restored)
	}
}

func TestAResizeThatDoesNotFitIsRefusedAndHoldsNothing(t *testing.T) {
	refusal := &planbudget.Refusal{Dimension: "CPU"}
	reserver := &recordingReserver{err: refusal}
	svc := &ProvisioningService{reserver: reserver}
	putBack, err := svc.reserveResize(context.Background(), &domain.DatabaseInstance{ProjectID: "p"}, standardPlan, "5Gi")
	if !errors.Is(err, error(refusal)) || putBack != nil {
		t.Fatalf("%v", err)
	}
}

func TestAResizeWithNoReserverIsUnchanged(t *testing.T) {
	putBack, err := (&ProvisioningService{}).reserveResize(context.Background(), &domain.DatabaseInstance{}, standardPlan, "5Gi")
	if err != nil || putBack == nil {
		t.Fatal("no budget wired must change nothing")
	}
	putBack()
}

func TestThePlanBudgetsProjectCountReplacesTheTierRowsWhenItAnswers(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	svc.SetProjectCap(func(context.Context, domain.TierType) (int, bool) { return 1, true })
	limit, err := svc.orgProjectLimit(context.Background(), domain.Free)
	if err != nil || limit != 1 {
		t.Fatalf("%d %v", limit, err)
	}
	svc.SetProjectCap(func(context.Context, domain.TierType) (int, bool) { return 0, false })
	if limit, _ := svc.orgProjectLimit(context.Background(), domain.Free); limit == 0 {
		t.Fatal("when the budget does not answer, the tier row still decides")
	}
}

func TestAProjectCreateThatDoesNotFitKeepsItsRefusal(t *testing.T) {
	refusal := &planbudget.Refusal{Dimension: "memory"}
	store := newBrokenStore(refusal)
	svc, _, _ := setupProvisioningTest(t)
	svc.store = store
	err := svc.createProjectRow(context.Background(), &domain.DatabaseInstance{OrgID: "o"}, domain.Free)
	var got *planbudget.Refusal
	if !errors.As(err, &got) || errors.Is(err, ErrProjectStoreUnavailable) {
		t.Fatalf("a refusal is not a store failure: %v", err)
	}
}

var unreadablePlan = config.TierConfig{CPU: "lots", Memory: "4Gi", Instances: 3, StorageSize: "50Gi"}

// While the budget is off a plan it cannot read must not stop a resize; the database is marked unrecorded.
func TestAnUnreadablePlanDoesNotStopAResizeWhileTheBudgetIsOff(t *testing.T) {
	reserver := &recordingReserver{inactive: true, held: planbudget.Reservation{PerCopy: planbudget.Size{CPUMilli: 500}, Copies: 1}}
	svc := &ProvisioningService{reserver: reserver}
	putBack, err := svc.reserveResize(context.Background(), &domain.DatabaseInstance{ProjectID: "p"}, unreadablePlan, "5Gi")
	if err != nil || putBack == nil {
		t.Fatalf("flag off: %v", err)
	}
	if len(reserver.asked) != 1 || reserver.asked[0] != (planbudget.Reservation{}) {
		t.Fatalf("the database is marked unrecorded: %+v", reserver.asked)
	}
	putBack()
	if len(reserver.restored) != 1 || reserver.restored[0].PerCopy.CPUMilli != 500 {
		t.Fatalf("a failed change still puts the old size back: %+v", reserver.restored)
	}
}

func TestAnUnreadablePlanStopsAResizeWhileTheBudgetIsOn(t *testing.T) {
	reserver := &recordingReserver{}
	svc := &ProvisioningService{reserver: reserver}
	putBack, err := svc.reserveResize(context.Background(), &domain.DatabaseInstance{ProjectID: "p"}, unreadablePlan, "5Gi")
	if err == nil || putBack != nil || len(reserver.asked) != 0 {
		t.Fatalf("flag on refuses what it cannot read: %v %+v", err, reserver.asked)
	}
}

func TestEachPartOfAnUnreadablePlanNamesItself(t *testing.T) {
	cases := map[string]struct {
		plan config.TierConfig
		disk string
		want string
	}{
		"cpu":    {config.TierConfig{CPU: "lots", Memory: "1Gi", Instances: 1}, "1Gi", "plan cpu"},
		"memory": {config.TierConfig{CPU: "1", Memory: "plenty", Instances: 1}, "1Gi", "plan memory"},
		"disk":   {config.TierConfig{CPU: "1", Memory: "1Gi", Instances: 1}, "big", "plan disk"},
	}
	for name, c := range cases {
		if _, err := resizeReservation(c.plan, c.disk); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAPlanWithNoCopiesCountsOne(t *testing.T) {
	got, err := resizeReservation(config.TierConfig{CPU: "1", Memory: "1Gi", Instances: 0}, "1Gi")
	if err != nil || got.Copies != 1 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestTheReserverIsWiredThroughItsSetter(t *testing.T) {
	reserver := &recordingReserver{}
	svc := &ProvisioningService{}
	svc.SetBudgetReserver(reserver)
	if svc.reserver != reserver {
		t.Fatal("the setter must keep the reserver")
	}
}

func TestAStoreFailureWhileLeavingASizeUnrecordedStopsTheResize(t *testing.T) {
	reserver := &recordingReserver{inactive: true, err: errors.New("db down")}
	svc := &ProvisioningService{reserver: reserver}
	if _, err := svc.reserveResize(context.Background(), &domain.DatabaseInstance{ProjectID: "p"}, config.TierConfig{CPU: "x"}, "1Gi"); err == nil {
		t.Fatal("a failed write must not let the resize go on")
	}
}

type failingRestore struct{ recordingReserver }

func (*failingRestore) RestoreDatabaseSize(context.Context, string, planbudget.Reservation) error {
	return errors.New("db down")
}

func TestAFailedPutBackIsLoggedAndDoesNotPanic(t *testing.T) {
	svc := &ProvisioningService{reserver: &failingRestore{}}
	putBack, err := svc.reserveResize(context.Background(), &domain.DatabaseInstance{ProjectID: "p"}, standardPlan, "60Gi")
	if err != nil {
		t.Fatal(err)
	}
	putBack()
}
