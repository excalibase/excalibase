package creditmeter

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/credits"
)

const gib = int64(1) << 30

var planPrices = Prices{Version: "v1", VCPUMonth: credits.FromDollars(12), RAMGiBMonth: credits.FromDollars(6), DiskGiBMonth: 120_000}

func slots(n int, slot Slot) []Slot {
	out := make([]Slot, n)
	for i := range out {
		out[i] = slot
	}
	return out
}

func TestAQuarterVCPUForAnHourIsThePlansGoldenFigure(t *testing.T) {
	// 12,000,000 x 0.25 x 12 / (730 x 12) = 4109.59, rounded half up once.
	got := HourCost(slots(12, Slot{CPUMilli: 250}), Allowance{}, planPrices)
	if got != 4110 {
		t.Fatalf("a quarter vCPU for one hour: want 4110 micros, got %d", got)
	}
}

func TestRoundingHappensOnceForTheHourNotPerSlot(t *testing.T) {
	// One slot is 342.47 micros: rounding each slot would give 12 x 342 = 4104, not 4110.
	perSlot := HourCost(slots(1, Slot{CPUMilli: 250}), Allowance{}, planPrices)
	if perSlot != 342 {
		t.Fatalf("one slot: want 342, got %d", perSlot)
	}
	if got := HourCost(slots(12, Slot{CPUMilli: 250}), Allowance{}, planPrices); got != 4110 {
		t.Fatalf("an hour must round once: want 4110, got %d", got)
	}
}

func TestAHalfMicroRoundsUp(t *testing.T) {
	// 1 vCPU for 12 slots costs price x 12 / 8760 = price / 730; a price of 365 is exactly 0.5.
	prices := Prices{Version: "v", VCPUMonth: 365}
	if got := HourCost(slots(12, Slot{CPUMilli: 1000}), Allowance{}, prices); got != 1 {
		t.Fatalf("0.5 micro must round up to 1, got %d", got)
	}
}

func TestOnlyTheSlotsPresentAreBilled(t *testing.T) {
	got := HourCost(slots(6, Slot{CPUMilli: 250}), Allowance{}, planPrices)
	if got != 2055 {
		t.Fatalf("half the slots bill half: want 2055 (2054.79), got %d", got)
	}
}

func TestTheAllowanceIsSubtractedPerSlotAndPerDimension(t *testing.T) {
	allowance := Allowance{CPUMilli: 1000, MemoryBytes: gib, DiskBytes: 10 * gib}
	inside := Slot{CPUMilli: 1000, MemoryBytes: gib, DiskBytes: 10 * gib}
	if got := HourCost(slots(12, inside), allowance, planPrices); got != 0 {
		t.Fatalf("usage inside the allowance is free, got %d", got)
	}
	// 0.5 vCPU, 1 GiB RAM and 5 GiB disk over the allowance for the whole hour:
	// (0.5x12 + 1x6 + 5x0.12) = 12.6 dollars a month -> 12,600,000 / 730 = 17260.27
	over := Slot{CPUMilli: 1500, MemoryBytes: 2 * gib, DiskBytes: 15 * gib}
	if got := HourCost(slots(12, over), allowance, planPrices); got != 17260 {
		t.Fatalf("over the allowance: want 17260, got %d", got)
	}
}

func TestADimensionBelowItsAllowanceNeverOffsetsAnother(t *testing.T) {
	allowance := Allowance{CPUMilli: 1000, MemoryBytes: gib, DiskBytes: 10 * gib}
	// CPU is 800 under; disk is 1 GiB over: only the disk bills.
	slot := Slot{CPUMilli: 200, MemoryBytes: 0, DiskBytes: 11 * gib}
	got := HourCost(slots(12, slot), allowance, planPrices)
	want := HourCost(slots(12, Slot{DiskBytes: gib}), Allowance{}, planPrices)
	if got != want || got == 0 {
		t.Fatalf("only the 1 GiB of disk bills: want %d, got %d", want, got)
	}
}

func TestAllowanceIsPerSlotSoABusySlotDoesNotBorrowFromAnIdleOne(t *testing.T) {
	allowance := Allowance{CPUMilli: 1000}
	mixed := append(slots(6, Slot{CPUMilli: 2000}), slots(6, Slot{CPUMilli: 0})...)
	steady := slots(12, Slot{CPUMilli: 1000})
	if HourCost(mixed, allowance, planPrices) == 0 {
		t.Fatal("six slots at 2 vCPU are billed for their extra vCPU even when the other six are idle")
	}
	if HourCost(steady, allowance, planPrices) != 0 {
		t.Fatal("a steady 1 vCPU is inside the allowance")
	}
}

func TestLargeSizesDoNotOverflow(t *testing.T) {
	huge := Slot{CPUMilli: 1_000_000, MemoryBytes: 1 << 50, DiskBytes: 1 << 50}
	if got := HourCost(slots(12, huge), Allowance{}, planPrices); got <= 0 {
		t.Fatalf("a huge reservation must cost a positive amount, got %d", got)
	}
}

func TestNoSlotsCostNothing(t *testing.T) {
	if got := HourCost(nil, Allowance{}, planPrices); got != 0 {
		t.Fatalf("no samples, no bill: got %d", got)
	}
}
