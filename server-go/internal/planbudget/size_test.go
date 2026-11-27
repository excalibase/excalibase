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
	cases := map[string]int64{"0.5": 500, "1": 1000, "250m": 250, "2": 2000}
	for in, want := range cases {
		if got, err := ParseCPUMilli(in); err != nil || got != want {
			t.Errorf("cpu %q: %d %v", in, got, err)
		}
	}
	mem := map[string]int64{"512Mi": 512 << 20, "1Gi": gib, "5Gi": 5 * gib, "1024": 1024}
	for in, want := range mem {
		if got, err := ParseBytes(in); err != nil || got != want {
			t.Errorf("bytes %q: %d %v", in, got, err)
		}
	}
	if _, err := ParseBytes("lots"); err == nil {
		t.Error("garbage must be refused, not read as zero")
	}
	if _, err := ParseCPUMilli("-1"); err == nil {
		t.Error("negative cpu")
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
