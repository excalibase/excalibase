package planbudget

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
)

// zeroPlans: Free budget 2000m / 2 GiB / 8 GiB, allowance 1000m / 1 GiB / 4 GiB.
var zeroPlans = config.PlanBudgetConfig{
	Free: config.PlanNumbers{
		CPUMilli: 2000, MemoryMiB: 2048, DiskGiB: 8, MaxServices: 6,
		AllowanceCPUMilli: 1000, AllowanceMemoryMiB: 1024, AllowanceDiskGiB: 4,
	},
	Standard: config.PlanNumbers{CPUMilli: 4000, MemoryMiB: 8192, DiskGiB: 50, MaxServices: 10},
}

func zeroGate(atZero bool) *Gate {
	gate := NewGate(func(context.Context) bool { return true }, zeroPlans)
	gate.SetZeroCheck(func(context.Context, Querier, string) (bool, error) { return atZero, nil })
	return gate
}

// an org holding one running database of 500m, 512 MiB, 1 GiB.
func holdingOneDatabase(t *testing.T) (Querier, *script) {
	t.Helper()
	return openScript(t, okLock, tierIs("FREE"), databases(runningDatabase("p1")), apps())
}

func refusalOf(t *testing.T, err error) *Refusal {
	t.Helper()
	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("want a refusal, got %v", err)
	}
	return refusal
}

func TestWithCreditTheCeilingIsTheBudget(t *testing.T) {
	db, _ := holdingOneDatabase(t)
	if err := zeroGate(false).Admit(context.Background(), db, "org-1", grows(Size{CPUMilli: 1500})); err != nil {
		t.Fatalf("500m held and 1500m asked fits the 2000m budget: %v", err)
	}
}

func TestAtZeroTheCeilingDropsToTheAllowance(t *testing.T) {
	db, _ := holdingOneDatabase(t)
	err := zeroGate(true).Admit(context.Background(), db, "org-1", grows(Size{CPUMilli: 501}))
	refusal := refusalOf(t, err)
	if refusal.Dimension != "CPU" || refusal.Left.CPUMilli != 500 || refusal.Budget.CPUMilli != 1000 {
		t.Fatalf("the ceiling is the 1000m allowance, 500m left: %+v", refusal)
	}
	db, _ = holdingOneDatabase(t)
	if err := zeroGate(true).Admit(context.Background(), db, "org-1", grows(Size{CPUMilli: 500})); err != nil {
		t.Fatalf("what the allowance still leaves is admitted: %v", err)
	}
}

func TestAtZeroTheRefusalSaysTheCreditIsUsedUp(t *testing.T) {
	db, _ := holdingOneDatabase(t)
	err := zeroGate(true).Admit(context.Background(), db, "org-1", grows(Size{MemoryBytes: 1 << 30}))
	refusal := refusalOf(t, err)
	if !refusal.CreditExhausted {
		t.Fatal("the refusal is marked as a credit refusal")
	}
	for _, want := range []string{"credit", "add credit"} {
		if !strings.Contains(strings.ToLower(refusal.Error()), want) {
			t.Fatalf("the message names %q: %s", want, refusal.Error())
		}
	}
	db, _ = holdingOneDatabase(t)
	err = zeroGate(false).Admit(context.Background(), db, "org-1", grows(Size{MemoryBytes: 4 << 30}))
	if refusalOf(t, err).CreditExhausted {
		t.Fatal("an org with credit that is over its budget is not told its credit is used up")
	}
}

func TestAtZeroDiskIsFrozenEvenBelowTheAllowance(t *testing.T) {
	db, _ := holdingOneDatabase(t)
	err := zeroGate(true).Admit(context.Background(), db, "org-1", grows(Size{DiskBytes: 1}))
	refusal := refusalOf(t, err)
	if refusal.Dimension != "disk" || refusal.Left.DiskBytes != 0 {
		t.Fatalf("1 GiB held of a 4 GiB allowance, yet one byte more is refused: %+v", refusal)
	}
	db, _ = holdingOneDatabase(t)
	if err := zeroGate(false).Admit(context.Background(), db, "org-1", grows(Size{DiskBytes: 1})); err != nil {
		t.Fatalf("with credit the same disk step fits: %v", err)
	}
}

func TestAtZeroTheServiceCountKeepsThePlansNumber(t *testing.T) {
	db, _ := openScript(t, okLock, tierIs("FREE"), databases(runningDatabase("p1")), apps())
	if err := zeroGate(true).Admit(context.Background(), db, "org-1", grows(Size{Services: 5})); err != nil {
		t.Fatalf("the allowance holds no service count, so the plan's 6 stands: %v", err)
	}
}

// R12: shrink, delete, pause and resize-down pass for an org at $0 that is over even its budget.
func TestAtZeroAndOverBudgetEveryShrinkingStepStillPasses(t *testing.T) {
	over := func() (Querier, *script) {
		return openScript(t, okLock, tierIs("FREE"),
			databases(runningDatabase("p1"), runningDatabase("p2"), runningDatabase("p3"), runningDatabase("p4"), runningDatabase("p5")), apps())
	}
	steps := map[string]Size{
		"pause":            {CPUMilli: -500, MemoryBytes: -(512 << 20)},
		"resize down":      {CPUMilli: -250, MemoryBytes: -(256 << 20), DiskBytes: 0},
		"shrink the disk":  {DiskBytes: -(1 << 30)},
		"delete a service": {CPUMilli: -500, MemoryBytes: -(512 << 20), DiskBytes: -(1 << 30), Services: -1},
		"change nothing":   {},
	}
	for name, step := range steps {
		db, _ := over()
		if err := zeroGate(true).Admit(context.Background(), db, "org-1", grows(step)); err != nil {
			t.Errorf("%s was refused for an org at $0: %v", name, err)
		}
	}
}

func TestAnUnreadableCreditStateRefusesGrowthInsteadOfAdmittingIt(t *testing.T) {
	boom := errors.New("connection reset")
	gate := NewGate(func(context.Context) bool { return true }, zeroPlans)
	gate.SetZeroCheck(func(context.Context, Querier, string) (bool, error) { return false, boom })
	db, _ := holdingOneDatabase(t)
	err := gate.Admit(context.Background(), db, "org-1", grows(Size{CPUMilli: 1}))
	if !errors.Is(err, boom) {
		t.Fatalf("the read error reaches the caller: %v", err)
	}
	var refusal *Refusal
	if errors.As(err, &refusal) {
		t.Fatal("a failed read is not a refusal")
	}
	db, _ = openScript(t, okLock)
	if err := gate.Admit(context.Background(), db, "org-1", grows(Size{CPUMilli: -1})); err != nil {
		t.Fatalf("a shrink never reads the credit state: %v", err)
	}
}

func TestAPlanWithNoBudgetIsNeverAtZero(t *testing.T) {
	db, _ := openScript(t, okLock, tierIs("ENTERPRISE"))
	if err := zeroGate(true).Admit(context.Background(), db, "org-1", grows(Size{CPUMilli: 1 << 30})); err != nil {
		t.Fatalf("a plan with no ceiling stays unlimited: %v", err)
	}
}

func TestTheReportOfAnOrgAtZeroShowsTheFreeAllowanceAsItsCeiling(t *testing.T) {
	db, _ := holdingOneDatabase(t)
	report, err := zeroGate(true).Report(context.Background(), db, "org-1")
	if err != nil {
		t.Fatal(err)
	}
	if report.Budget.CPUMilli != 1000 || report.Budget.MemoryBytes != 1024<<20 || report.Left.CPUMilli != 500 {
		t.Fatalf("the meters show what the org may really hold now: %+v", report)
	}
	if report.Budget.DiskBytes != 1<<30 || report.Left.DiskBytes != 0 {
		t.Fatalf("the disk is frozen at what it holds: %+v", report)
	}
	db, _ = holdingOneDatabase(t)
	with, err := zeroGate(false).Report(context.Background(), db, "org-1")
	if err != nil || with.Budget.CPUMilli != 2000 {
		t.Fatalf("with credit the plan's budget shows: %+v %v", with, err)
	}
}
