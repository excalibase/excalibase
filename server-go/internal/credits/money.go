package credits

import (
	"fmt"
	"strings"
)

// Micros is money in millionths of a dollar. Every amount in the ledger is one;
// there are no floats anywhere.
type Micros int64

const microsPerDollar Micros = 1_000_000

// FromDollars is a whole number of dollars in micros.
func FromDollars(dollars int64) Micros { return Micros(dollars) * microsPerDollar }

// Dollars writes the amount as dollars with at least two decimals and no
// trailing zeros beyond them.
func (m Micros) Dollars() string {
	sign := ""
	if m < 0 {
		sign, m = "-", -m
	}
	whole, fraction := m/microsPerDollar, m%microsPerDollar
	decimals := strings.TrimRight(fmt.Sprintf("%06d", fraction), "0")
	for len(decimals) < 2 {
		decimals += "0"
	}
	return fmt.Sprintf("%s%d.%s", sign, whole, decimals)
}
