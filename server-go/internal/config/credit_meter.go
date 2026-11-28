package config

import (
	"encoding/json"
	"fmt"
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
	if len(errs) > 0 {
		cfg.err = fmt.Errorf("credit meter settings: %s", strings.Join(errs, "; "))
	}
	return cfg
}

func (c AppConfig) validateCreditMeter() error { return c.CreditMeter.err }
