// Package creditmeter samples what an org reserves every five minutes and
// bills it to the org's credit once an hour (EXC-866).
package creditmeter

import (
	"math/big"
	"time"

	"github.com/excalibase/provisioning-poc/internal/credits"
)

const (
	// SlotLength is how often usage is sampled; an hour holds SlotsPerHour slots.
	SlotLength   = 5 * time.Minute
	SlotsPerHour = int64(time.Hour / SlotLength)
	// HoursPerMonth turns a monthly price into an hourly one, as AWS and Railway do.
	HoursPerMonth = 730
)

// Prices are what a month of each resource costs, in micro-dollars. They are
// configuration; Version is written on every charge so a price change is traceable.
type Prices struct {
	Version      string
	VCPUMonth    credits.Micros
	RAMGiBMonth  credits.Micros
	DiskGiBMonth credits.Micros
}

// Slot is what an org reserved at one sample, with the allowance of the plan it
// was on then, so a later plan change never reprices an hour already sampled.
type Slot struct {
	CPUMilli    int64
	MemoryBytes int64
	DiskBytes   int64
	Allowance   Allowance
}

// Allowance is the part of a plan that is never charged, subtracted from every slot.
type Allowance struct {
	CPUMilli    int64
	MemoryBytes int64
	DiskBytes   int64
}

const bytesPerGiB = int64(1) << 30

// Usage is an hour's reservation above the allowance, summed over its slots.
type Usage struct {
	CPUMilliSlots   int64
	MemoryByteSlots int64
	DiskByteSlots   int64
}

// SumOver adds up, slot by slot, what is reserved above the allowance the slot was sampled under. A slot
// below its allowance in one dimension never offsets another dimension or slot.
func SumOver(slots []Slot) Usage {
	var used Usage
	for _, slot := range slots {
		used.CPUMilliSlots += over(slot.CPUMilli, slot.Allowance.CPUMilli)
		used.MemoryByteSlots += over(slot.MemoryBytes, slot.Allowance.MemoryBytes)
		used.DiskByteSlots += over(slot.DiskBytes, slot.Allowance.DiskBytes)
	}
	return used
}

// Cost is the usage times the monthly prices, over 730 hours of 12 slots. The
// sum is exact (rational) and rounded half up once, for the whole hour.
func (u Usage) Cost(prices Prices) credits.Micros {
	total := new(big.Rat)
	total.Add(total, share(u.CPUMilliSlots, 1000, prices.VCPUMonth))
	total.Add(total, share(u.MemoryByteSlots, bytesPerGiB, prices.RAMGiBMonth))
	total.Add(total, share(u.DiskByteSlots, bytesPerGiB, prices.DiskGiBMonth))
	total.Quo(total, new(big.Rat).SetInt64(HoursPerMonth*SlotsPerHour))
	return roundHalfUp(total)
}

// HourCost is what the slots of one hour cost. A slot that was not sampled bills nothing.
func HourCost(slots []Slot, prices Prices) credits.Micros {
	return SumOver(slots).Cost(prices)
}

func over(reserved, allowed int64) int64 { return max(0, reserved-allowed) }

// share is amount/unit of a monthly price, exactly.
func share(amount, unit int64, monthly credits.Micros) *big.Rat {
	return new(big.Rat).SetFrac(new(big.Int).Mul(big.NewInt(amount), big.NewInt(int64(monthly))), big.NewInt(unit))
}

func roundHalfUp(value *big.Rat) credits.Micros {
	doubled := new(big.Int).Mul(value.Num(), big.NewInt(2))
	denominator := new(big.Int).Mul(value.Denom(), big.NewInt(2))
	doubled.Add(doubled, value.Denom())
	return credits.Micros(doubled.Div(doubled, denominator).Int64())
}
