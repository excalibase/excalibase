package credits

import (
	"cmp"
	"slices"
	"time"
)

// Draw is how much one lot pays toward a debit.
type Draw struct {
	LotID  int64
	Amount Micros
}

// PlanDraw splits a debit for a period ending at periodEnd across the lots
// that are still good then, soonest-expiring first, and never takes more than
// they hold. A lot that expires before the period ends cannot pay for it.
func PlanDraw(lots []Lot, want Micros, periodEnd time.Time) (draws []Draw, taken Micros) {
	usable := make([]Lot, 0, len(lots))
	for _, lot := range lots {
		if lot.Remaining > 0 && (lot.ExpiresAt == nil || !lot.ExpiresAt.Before(periodEnd)) {
			usable = append(usable, lot)
		}
	}
	slices.SortStableFunc(usable, compareExpiry)
	for _, lot := range usable {
		if taken >= want {
			break
		}
		amount := min(lot.Remaining, want-taken)
		draws = append(draws, Draw{LotID: lot.ID, Amount: amount})
		taken += amount
	}
	return draws, taken
}

// compareExpiry orders lots soonest-expiring first, lots that never expire
// last, and ties by id so the order is the same on every run.
func compareExpiry(a, b Lot) int {
	switch {
	case a.ExpiresAt == nil && b.ExpiresAt == nil:
		return cmp.Compare(a.ID, b.ID)
	case a.ExpiresAt == nil:
		return 1
	case b.ExpiresAt == nil:
		return -1
	}
	if byTime := a.ExpiresAt.Compare(*b.ExpiresAt); byTime != 0 {
		return byTime
	}
	return cmp.Compare(a.ID, b.ID)
}
