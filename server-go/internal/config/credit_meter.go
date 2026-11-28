package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// CreditMeterConfig is the price table behind FEATURE_CREDITBURN
// (EXC-866). Prices are micro-dollars per month. None of it is decided in code, and a
// mistyped value stops the boot: a silent default would quietly change what people are billed.
type CreditMeterConfig struct {
	// PriceVersion is written on every charge so a price change can be traced.
	PriceVersion       string
	VCPUMonthMicros    int64
	RAMGiBMonthMicros  int64
	DiskGiBMonthMicros int64
	// GraceDays is how long compute above the free allowance keeps running once the credit is used up.
	GraceDays int
	// TransitionMaxAgeMinutes is how long a service may sit in a changing state (starting, pausing,
	// resuming, restoring) and still hold its org out of credit pauses; after that it is ignored.
	TransitionMaxAgeMinutes int

	err error
}

type creditPricesJSON struct {
	Version            string `json:"version"`
	VCPUMonthMicros    *int64 `json:"vcpuMonthMicros"`
	RAMGiBMonthMicros  *int64 `json:"ramGiBMonthMicros"`
	DiskGiBMonthMicros *int64 `json:"diskGiBMonthMicros"`
}

// defaultCreditPrices are $12 per vCPU-month, $6 per GiB-month of RAM and $0.12 per GiB-month of disk.
const defaultCreditPrices = `{"version":"v1","vcpuMonthMicros":12000000,"ramGiBMonthMicros":6000000,"diskGiBMonthMicros":120000}`

func loadCreditMeter() CreditMeterConfig {
	var errs []string
	cfg := CreditMeterConfig{}
	raw := strings.TrimSpace(envOr("CREDIT_PRICES", defaultCreditPrices))
	var prices creditPricesJSON
	if err := json.Unmarshal([]byte(raw), &prices); err != nil {
		errs = append(errs, fmt.Sprintf("CREDIT_PRICES is not valid JSON: %v", err))
	} else if prices.Version == "" || prices.VCPUMonthMicros == nil || prices.RAMGiBMonthMicros == nil || prices.DiskGiBMonthMicros == nil ||
		*prices.VCPUMonthMicros < 0 || *prices.RAMGiBMonthMicros < 0 || *prices.DiskGiBMonthMicros < 0 {
		errs = append(errs, "CREDIT_PRICES needs a version and a price of at least 0 for vcpuMonthMicros, ramGiBMonthMicros and diskGiBMonthMicros")
	} else {
		cfg.PriceVersion = prices.Version
		cfg.VCPUMonthMicros, cfg.RAMGiBMonthMicros, cfg.DiskGiBMonthMicros = *prices.VCPUMonthMicros, *prices.RAMGiBMonthMicros, *prices.DiskGiBMonthMicros
	}
	cfg.GraceDays = graceDays(&errs)
	cfg.TransitionMaxAgeMinutes = transitionMaxAgeMinutes(&errs)
	if len(errs) > 0 {
		cfg.err = fmt.Errorf("credit meter settings: %s", strings.Join(errs, "; "))
	}
	return cfg
}

const (
	defaultGraceDays = 7
	maxGraceDays     = 365
)

// graceDays reads CREDIT_GRACE_DAYS: whole days from 0 (pause at once) to a year.
func graceDays(errs *[]string) int {
	raw := strings.TrimSpace(envOr("CREDIT_GRACE_DAYS", ""))
	if raw == "" {
		return defaultGraceDays
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 0 || days > maxGraceDays {
		*errs = append(*errs, fmt.Sprintf("CREDIT_GRACE_DAYS=%q is not a whole number of days from 0 to %d", raw, maxGraceDays))
		return defaultGraceDays
	}
	return days
}

const (
	defaultTransitionMaxAgeMinutes = 30
	maxTransitionMaxAgeMinutes     = 24 * 60
)

func transitionMaxAgeMinutes(errs *[]string) int {
	raw := strings.TrimSpace(envOr("CREDIT_TRANSITION_MAX_AGE_MINUTES", ""))
	if raw == "" {
		return defaultTransitionMaxAgeMinutes
	}
	minutes, err := strconv.Atoi(raw)
	if err != nil || minutes < 1 || minutes > maxTransitionMaxAgeMinutes {
		*errs = append(*errs, fmt.Sprintf("CREDIT_TRANSITION_MAX_AGE_MINUTES=%q is not a whole number of minutes from 1 to %d", raw, maxTransitionMaxAgeMinutes))
		return defaultTransitionMaxAgeMinutes
	}
	return minutes
}

func (c AppConfig) validateCreditMeter() error { return c.CreditMeter.err }
