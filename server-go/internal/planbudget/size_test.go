package planbudget

import (
	"errors"
	"strings"
	"testing"
)

const gib = int64(1) << 30

var freeBudget = Size{CPUMilli: 2000, MemoryBytes: 4 * gib, DiskBytes: 20 * gib, Services: 6}

func TestDecideAdmitsWhatFitsAndNamesWhatIsLeftWhenItDoesNot(t *testing.T) {
	used := Size{CPUMilli: 1500, MemoryBytes: 3 * gib, DiskBytes: 5 * gib}
	if err := Decide(used, Size{CPUMilli: 500, MemoryBytes: gib, DiskBytes: gib}, freeBudget); err != nil {
		t.Fatalf("exactly the budget fits: %v", err)
	}
	err := Decide(used, Size{CPUMilli: 600}, freeBudget)
	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("one milli-CPU over must be refused: %v", err)
	}
	if refusal.Left.CPUMilli != 500 || refusal.Need.CPUMilli != 600 || refusal.Budget != freeBudget {
		t.Fatalf("%+v", refusal)
	}
	if !strings.Contains(refusal.Error(), "CPU") || !strings.Contains(refusal.Error(), "left") {
		t.Fatalf("the message names what is left: %q", refusal.Error())
	}
}

func TestDecideNamesEveryDimensionThatIsShort(t *testing.T) {
	err := Decide(Size{MemoryBytes: 4 * gib}, Size{MemoryBytes: 1}, freeBudget)
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Dimension != "memory" {
		t.Fatalf("%v", err)
	}
	err = Decide(Size{DiskBytes: 20 * gib}, Size{DiskBytes: 1}, freeBudget)
	if !errors.As(err, &refusal) || refusal.Dimension != "disk" {
		t.Fatalf("%v", err)
	}
}

func TestShrinkingAlwaysPassesEvenWhenFarOverBudget(t *testing.T) {
	over := Size{CPUMilli: 9000, MemoryBytes: 40 * gib, DiskBytes: 90 * gib}
	for _, delta := range []Size{{}, {CPUMilli: -1}, {CPUMilli: -500, MemoryBytes: -gib, DiskBytes: -gib}} {
		if err := Decide(over, delta, freeBudget); err != nil {
			t.Fatalf("delta %+v must pass: %v", delta, err)
		}
	}
}

func TestAMixedChangeIsRefusedOnlyOnTheDimensionThatGrows(t *testing.T) {
	used := Size{CPUMilli: 2000, MemoryBytes: gib}
	if err := Decide(used, Size{CPUMilli: -1000, MemoryBytes: gib}, freeBudget); err != nil {
		t.Fatalf("CPU shrinks and memory still fits: %v", err)
	}
	if err := Decide(used, Size{CPUMilli: 1, MemoryBytes: -gib}, freeBudget); err == nil {
		t.Fatal("CPU grows past its ceiling while memory shrinks: refuse")
	}
}

func TestSizeArithmetic(t *testing.T) {
	a, b := Size{1, 2, 3, 1}, Size{10, 20, 30, 2}
	if a.Add(b) != (Size{11, 22, 33, 3}) || b.Sub(a) != (Size{9, 18, 27, 1}) || a.Times(3) != (Size{3, 6, 9, 1}) {
		t.Fatal("arithmetic")
	}
	if (Size{}).IsZero() != true || a.IsZero() {
		t.Fatal("IsZero")
	}
}

func TestParseQuantities(t *testing.T) {
	expectQuantities(t, "cpu", ParseCPUMilli, map[string]int64{"0.5": 500, "1": 1000, "250m": 250, "2": 2000})
	expectQuantities(t, "bytes", ParseBytes, map[string]int64{"512Mi": 512 << 20, "1Gi": gib, "5Gi": 5 * gib, "1024": 1024})
	// Anything the admin tier editor stores (resource.ParseQuantity) is a size here too.
	expectQuantities(t, "bytes", ParseBytes, map[string]int64{"1.5Gi": gib + gib/2, "4G": 4_000_000_000, "500M": 500_000_000, "2e3": 2000, "1k": 1000})
	expectQuantities(t, "cpu", ParseCPUMilli, map[string]int64{"1.5": 1500, "100m": 100, "1500m": 1500, "0.25": 250})
	if _, err := ParseBytes("lots"); err == nil {
		t.Error("garbage must be refused, not read as zero")
	}
	if _, err := ParseCPUMilli("-1"); err == nil {
		t.Error("negative cpu")
	}
}

func expectQuantities(t *testing.T, kind string, parse func(string) (int64, error), cases map[string]int64) {
	t.Helper()
	for in, want := range cases {
		if got, err := parse(in); err != nil || got != want {
			t.Errorf("%s %q: %d %v", kind, in, got, err)
		}
	}
}

func TestTheServiceCountIsALimitToo(t *testing.T) {
	err := Decide(Size{Services: 6}, Size{Services: 1}, freeBudget)
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Dimension != "services" {
		t.Fatalf("a seventh service on a plan of six: %v", err)
	}
	if err := Decide(Size{Services: 5}, Size{Services: 1}, freeBudget); err != nil {
		t.Fatal(err)
	}
}

func TestARefusalReadsInTheUnitsOfTheDimensionThatIsShort(t *testing.T) {
	ceiling := Size{CPUMilli: 1000, MemoryBytes: 1 << 30, DiskBytes: 2 << 30, Services: 3}
	cases := map[string]struct {
		delta, used Size
		want        []string
	}{
		"CPU":      {Size{CPUMilli: 750}, Size{CPUMilli: 500}, []string{"0.75 vCPU", "0.50 vCPU", "1.00 vCPU"}},
		"memory":   {Size{MemoryBytes: 512 << 20}, Size{MemoryBytes: 768 << 20}, []string{"512 MiB", "256 MiB", "1 GiB"}},
		"disk":     {Size{DiskBytes: 3 << 30}, Size{DiskBytes: 1 << 30}, []string{"3 GiB", "1 GiB", "2 GiB"}},
		"services": {Size{Services: 2}, Size{Services: 2}, []string{"2 more services", "only 1 is left", "allows 3"}},
	}
	for name, c := range cases {
		err := Decide(c.used, c.delta, ceiling)
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Dimension != name {
			t.Errorf("%s: %v", name, err)
			continue
		}
		for _, fragment := range c.want {
			if !strings.Contains(err.Error(), fragment) {
				t.Errorf("%s: %q is missing %q", name, err.Error(), fragment)
			}
		}
	}
}

func TestAnOrgAlreadyOverItsPlanIsToldNothingIsLeft(t *testing.T) {
	err := Decide(Size{CPUMilli: 2000}, Size{CPUMilli: 1}, Size{CPUMilli: 1000})
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Left.CPUMilli != 0 {
		t.Fatalf("%v", err)
	}
}

func TestSizeSubtractionAndEmptiness(t *testing.T) {
	if got := (Size{CPUMilli: 5, Services: 2}).Sub(Size{CPUMilli: 3, Services: 1}); got != (Size{CPUMilli: 2, Services: 1}) {
		t.Fatalf("%+v", got)
	}
	if !(Size{}).IsZero() || (Size{Services: 1}).IsZero() {
		t.Fatal("IsZero")
	}
}

func TestARefusalCarriesTheWholeStepNotOnlyTheDimensionThatDidNotFit(t *testing.T) {
	step := Size{CPUMilli: 600, MemoryBytes: gib, DiskBytes: 2 * gib, Services: 1}
	err := Decide(Size{CPUMilli: 1500, MemoryBytes: gib}, step, freeBudget)
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Dimension != "CPU" {
		t.Fatalf("%v", err)
	}
	if refusal.Step != step {
		t.Fatalf("the step is all of what was asked for, so a caller can tell whether it fits alone: %+v", refusal.Step)
	}
}
