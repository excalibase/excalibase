package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/planbudget"
)

type recordingReserver struct {
	asked    []planbudget.Reservation
	restored []planbudget.Reservation
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
