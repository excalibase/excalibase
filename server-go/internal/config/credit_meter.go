package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// AllowanceNumbers is what a plan never charges for: the part of its budget
// that is free. All zero means every reservation is charged.
type AllowanceNumbers struct {
	CPUMilli  int
	MemoryMiB int
	DiskGiB   int
}

// CreditMeterConfig is the price table and the free allowances behind FEATURE_CREDITBURN
// (EXC-866). Prices are micro-dollars per month. None of it is decided in code, and a
// mistyped value stops the boot: a silent default would quietly change what people are billed.
type CreditMeterConfig struct {
	// PriceVersion is written on every charge so a price change can be traced.
	PriceVersion       string
	VCPUMonthMicros    int64
	RAMGiBMonthMicros  int64
	DiskGiBMonthMicros int64
	Free               AllowanceNumbers
	Standard           AllowanceNumbers

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
	number := func(key string, fallback int) int {
		text := strings.TrimSpace(os.Getenv(key))
		if text == "" {
			return fallback
		}
		n, err := strconv.Atoi(text)
		if err != nil || n < 0 {
			errs = append(errs, fmt.Sprintf("%s=%q is not a whole number of at least 0", key, text))
			return fallback
		}
		return n
	}
	allowance := func(plan string, d AllowanceNumbers) AllowanceNumbers {
		prefix := "CREDIT_ALLOWANCE_" + plan
		return AllowanceNumbers{
			CPUMilli:  number(prefix+"_CPU_MILLI", d.CPUMilli),
			MemoryMiB: number(prefix+"_MEMORY_MIB", d.MemoryMiB),
			DiskGiB:   number(prefix+"_DISK_GIB", d.DiskGiB),
		}
	}
	cfg.Free = allowance("FREE", AllowanceNumbers{CPUMilli: 1000, MemoryMiB: 1024, DiskGiB: 10})
	cfg.Standard = allowance("STANDARD", AllowanceNumbers{})
	if len(errs) > 0 {
		cfg.err = fmt.Errorf("credit meter settings: %s", strings.Join(errs, "; "))
	}
	return cfg
}

func (c AppConfig) validateCreditMeter() error { return c.CreditMeter.err }
