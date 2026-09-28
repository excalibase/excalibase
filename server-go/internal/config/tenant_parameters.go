package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
)

// ErrTenantParameter refuses a Postgres parameter a tenant may not set, or a
// value outside what the platform allows for it.
var ErrTenantParameter = errors.New("postgres parameter not allowed")

type parameterRule func(value string, tier TierConfig) error

// tenantParameters is every Postgres setting a tenant may choose, each with
// the values it may take. Anything absent is the platform's: timeouts,
// connection limits, preloaded libraries, logging, TLS, archiving.
var tenantParameters = map[string]parameterRule{
	"work_mem":                      memoryWithin(64*kib, 16),
	"maintenance_work_mem":          memoryWithin(mib, 4),
	"effective_cache_size":          memoryWithin(mib, 1),
	"random_page_cost":              realWithin(0.1, 100),
	"seq_page_cost":                 realWithin(0.1, 100),
	"effective_io_concurrency":      integerWithin(0, 1000),
	"default_statistics_target":     integerWithin(1, 10000),
	"jit":                           oneOf("on", "off"),
	"default_transaction_isolation": oneOf("read committed", "repeatable read", "serializable"),
	"lock_timeout":                  durationWithin(0, time.Hour),
	"deadlock_timeout":              durationWithin(time.Millisecond, time.Minute),
}

// TenantTunableParameter reports whether a tenant may set the named parameter.
func TenantTunableParameter(name string) bool {
	_, ok := tenantParameters[name]
	return ok
}

// ValidateTenantParameters refuses the whole set if any parameter is not the
// tenant's to set or is out of its bounds for the tier.
func ValidateTenantParameters(params map[string]string, tier TierConfig) error {
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		rule, ok := tenantParameters[name]
		if !ok {
			return fmt.Errorf("%w: %q is set by the platform", ErrTenantParameter, name)
		}
		if err := rule(params[name], tier); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrTenantParameter, name, err)
		}
	}
	return nil
}

const (
	kib = int64(1024)
	mib = 1024 * kib
)

var memoryPattern = regexp.MustCompile(`^([0-9]+)(kB|MB|GB|TB)$`)

var memoryUnits = map[string]int64{"kB": kib, "MB": mib, "GB": 1024 * mib, "TB": 1024 * 1024 * mib}

// memoryWithin allows at least floor bytes and at most the tier's memory
// divided by share, so the setting cannot outgrow the pod it runs in.
func memoryWithin(floor int64, share int64) parameterRule {
	return func(value string, tier TierConfig) error {
		match := memoryPattern.FindStringSubmatch(value)
		if match == nil {
			return errors.New("must be a whole number with a unit of kB, MB, GB or TB")
		}
		amount, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			return errors.New("is too large")
		}
		limit, err := tierMemoryBytes(tier)
		if err != nil {
			return err
		}
		unit := memoryUnits[match[2]]
		if amount > limit/unit || amount*unit < floor || amount*unit > limit/share {
			return fmt.Errorf("must be between %dkB and %dkB on this tier", floor/kib, limit/share/kib)
		}
		return nil
	}
}

func tierMemoryBytes(tier TierConfig) (int64, error) {
	if tier.Memory == "" {
		return 0, errors.New("the tier defines no memory to bound it by")
	}
	quantity, err := resource.ParseQuantity(tier.Memory)
	if err != nil {
		return 0, fmt.Errorf("the tier's memory %q cannot be read", tier.Memory)
	}
	return quantity.Value(), nil
}

func realWithin(low, high float64) parameterRule {
	return func(value string, _ TierConfig) error {
		number, err := strconv.ParseFloat(value, 64)
		if err != nil || !(number >= low && number <= high) {
			return fmt.Errorf("must be a number between %g and %g", low, high)
		}
		return nil
	}
}

func integerWithin(low, high int64) parameterRule {
	return func(value string, _ TierConfig) error {
		number, err := strconv.ParseInt(value, 10, 64)
		if err != nil || number < low || number > high {
			return fmt.Errorf("must be a whole number between %d and %d", low, high)
		}
		return nil
	}
}

func oneOf(choices ...string) parameterRule {
	return func(value string, _ TierConfig) error {
		if !slices.Contains(choices, value) {
			return fmt.Errorf("must be one of %s", strings.Join(choices, ", "))
		}
		return nil
	}
}

var durationPattern = regexp.MustCompile(`^([0-9]+)(ms|s|min|h)$`)

var durationUnits = map[string]time.Duration{"ms": time.Millisecond, "s": time.Second, "min": time.Minute, "h": time.Hour}

func durationWithin(low, high time.Duration) parameterRule {
	return func(value string, _ TierConfig) error {
		match := durationPattern.FindStringSubmatch(value)
		bound := fmt.Errorf("must be between %s and %s, with a unit of ms, s, min or h", low, high)
		if match == nil {
			return bound
		}
		amount, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || amount > int64(high/time.Millisecond) {
			return bound
		}
		length := time.Duration(amount) * durationUnits[match[2]]
		if length < low || length > high {
			return bound
		}
		return nil
	}
}

// TenantTunableParameterNames lists, sorted, every setting a tenant may choose.
func TenantTunableParameterNames() []string {
	names := make([]string, 0, len(tenantParameters))
	for name := range tenantParameters {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
