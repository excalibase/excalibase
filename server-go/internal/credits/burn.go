package credits

import (
	"math/big"
	"time"
)

const (
	burnDays = 7
	// minObserved keeps a first, partial hour of usage from being scaled up into a huge daily rate.
	minObserved = time.Hour
	// farOff is the run-out beyond which no date is shown.
	farOff = 10 * 365 * 24 * time.Hour
)

// DayBurn is what usage drew on one UTC day.
type DayBurn struct {
	Day    time.Time
	Micros Micros
}

// UsageBurn is the raw usage of the last days as the ledger keeps it; Since is
// when the first of it began.
type UsageBurn struct {
	Days  []DayBurn
	Since *time.Time
}

// DayBurnView is one day of the burn chart.
type DayBurnView struct {
	Day    string `json:"day"`
	Micros Micros `json:"micros"`
}

// Burn is how fast an org spends its credit and when that runs out at this rate.
type Burn struct {
	PerDay   Micros        `json:"perDayMicros"`
	Days     []DayBurnView `json:"days"`
	RunOutAt *time.Time    `json:"runOutAt"`
}

// BurnWindowStart is the first UTC day of the seven the burn covers.
func BurnWindowStart(now time.Time) time.Time {
	today := now.UTC().Truncate(24 * time.Hour)
	return today.AddDate(0, 0, -(burnDays - 1))
}

// ComputeBurn turns the last seven days of usage into a daily rate and a
// projected run-out. The rate is the usage over the time observed (never
// longer than the window, never shorter than an hour), so a new org is not
// averaged against days before it started.
func ComputeBurn(usage UsageBurn, balance Micros, now time.Time) Burn {
	now = now.UTC()
	start := BurnWindowStart(now)
	byDay := map[string]Micros{}
	var total Micros
	for _, day := range usage.Days {
		byDay[day.Day.UTC().Format("2006-01-02")] += day.Micros
		total += day.Micros
	}
	burn := Burn{Days: make([]DayBurnView, 0, burnDays)}
	for i := range burnDays {
		key := start.AddDate(0, 0, i).Format("2006-01-02")
		burn.Days = append(burn.Days, DayBurnView{Day: key, Micros: byDay[key]})
	}
	if total <= 0 {
		return burn
	}
	observedFrom := start
	if usage.Since != nil && usage.Since.After(start) {
		observedFrom = usage.Since.UTC()
	}
	observed := max(now.Sub(observedFrom), minObserved)
	burn.PerDay = Micros(scale(int64(total), int64(24*time.Hour), int64(observed)).Int64())
	if balance > 0 && burn.PerDay > 0 {
		untilEmpty := scale(int64(balance), int64(24*time.Hour), int64(burn.PerDay))
		if untilEmpty.IsInt64() && time.Duration(untilEmpty.Int64()) <= farOff {
			runOut := now.Add(time.Duration(untilEmpty.Int64()))
			burn.RunOutAt = &runOut
		}
	}
	return burn
}

// scale is amount x numerator / denominator, exact until the final round down.
func scale(amount, numerator, denominator int64) *big.Int {
	product := new(big.Int).Mul(big.NewInt(amount), big.NewInt(numerator))
	return product.Div(product, big.NewInt(denominator))
}
