package config

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/behaviour"
)

// behaviourEnvNames are the variables behaviour review reads; each overrides one default.
var behaviourEnvNames = []string{
	"BEHAVIOUR_WINDOW_MINUTES", "BEHAVIOUR_SUSTAINED_PERCENT", "BEHAVIOUR_SUSTAINED_MINUTES",
	"BEHAVIOUR_SYMMETRY_LOW", "BEHAVIOUR_SYMMETRY_HIGH", "BEHAVIOUR_SYMMETRY_MIN_PERCENT",
	"BEHAVIOUR_DAILY_VOLUME_PERCENT", "BEHAVIOUR_MONTHLY_VOLUME_PERCENT",
	"BEHAVIOUR_TIER_MBPS", "BEHAVIOUR_DEFAULT_MBPS", "BEHAVIOUR_EVAL_MINUTES",
	"BEHAVIOUR_MUTE_DAYS", "BEHAVIOUR_GROWTH_FACTOR", "BEHAVIOUR_DISMISSED_RETENTION_DAYS",
}

// loadBehaviour starts from the defaults and applies each variable that holds a
// usable value. A value that is not a positive number (or a pair that makes no
// sense together) is logged and the default stays: a typo must not turn a
// threshold into zero, which would flag everything or nothing.
func loadBehaviour() behaviour.Config {
	cfg := behaviour.DefaultConfig()
	positiveInt(&cfg.WindowMinutes, "BEHAVIOUR_WINDOW_MINUTES")
	positiveFloat(&cfg.SustainedPercent, "BEHAVIOUR_SUSTAINED_PERCENT")
	positiveInt(&cfg.SustainedMinutes, "BEHAVIOUR_SUSTAINED_MINUTES")
	positiveFloat(&cfg.SymmetryMinPercent, "BEHAVIOUR_SYMMETRY_MIN_PERCENT")
	positiveFloat(&cfg.DailyVolumePercent, "BEHAVIOUR_DAILY_VOLUME_PERCENT")
	positiveFloat(&cfg.MonthlyVolumePercent, "BEHAVIOUR_MONTHLY_VOLUME_PERCENT")
	positiveFloat(&cfg.DefaultReferenceMbps, "BEHAVIOUR_DEFAULT_MBPS")
	positiveInt(&cfg.MuteDays, "BEHAVIOUR_MUTE_DAYS")
	positiveInt(&cfg.DismissedRetentionDays, "BEHAVIOUR_DISMISSED_RETENTION_DAYS")
	var evalMinutes int
	if positiveInt(&evalMinutes, "BEHAVIOUR_EVAL_MINUTES") {
		cfg.EvalEvery = time.Duration(evalMinutes) * time.Minute
	}
	var growth float64
	if positiveFloat(&growth, "BEHAVIOUR_GROWTH_FACTOR") {
		if growth < 1 {
			log.Printf("config: BEHAVIOUR_GROWTH_FACTOR=%v is under 1 and would end every mute at once; keeping %v", growth, cfg.GrowthFactor)
		} else {
			cfg.GrowthFactor = growth
		}
	}
	applySymmetryBand(&cfg)
	if raw := strings.TrimSpace(os.Getenv("BEHAVIOUR_TIER_MBPS")); raw != "" {
		if tiers, ok := parseTierMbps(raw); ok {
			cfg.ReferenceMbps = tiers
		} else {
			log.Printf("config: BEHAVIOUR_TIER_MBPS=%q is not tier=Mbit/s pairs; keeping the defaults", raw)
		}
	}
	if cfg.SustainedMinutes > cfg.WindowMinutes {
		log.Printf("config: BEHAVIOUR_SUSTAINED_MINUTES=%d is more than the %d-minute window, so the signal could never fire; keeping the defaults for both",
			cfg.SustainedMinutes, cfg.WindowMinutes)
		defaults := behaviour.DefaultConfig()
		cfg.SustainedMinutes, cfg.WindowMinutes = defaults.SustainedMinutes, defaults.WindowMinutes
	}
	return cfg
}

func applySymmetryBand(cfg *behaviour.Config) {
	low, high := cfg.SymmetryLow, cfg.SymmetryHigh
	lowSet := positiveFloat(&low, "BEHAVIOUR_SYMMETRY_LOW")
	highSet := positiveFloat(&high, "BEHAVIOUR_SYMMETRY_HIGH")
	if (lowSet || highSet) && low >= high {
		log.Printf("config: the symmetry band %v..%v is empty; keeping %v..%v", low, high, cfg.SymmetryLow, cfg.SymmetryHigh)
		return
	}
	cfg.SymmetryLow, cfg.SymmetryHigh = low, high
}

// positiveInt sets *target from the variable when it is a whole number above zero.
func positiveInt(target *int, name string) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return false
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		log.Printf("config: %s=%q is not a whole number above zero; keeping %d", name, raw, *target)
		return false
	}
	*target = value
	return true
}

func positiveFloat(target *float64, name string) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return false
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value <= 0 {
		log.Printf("config: %s=%q is not a number above zero; keeping %v", name, raw, *target)
		return false
	}
	*target = value
	return true
}

// parseTierMbps reads "free=10,standard=100": a reference rate per tier. Any
// malformed pair refuses the whole value.
func parseTierMbps(raw string) (map[string]float64, bool) {
	tiers := map[string]float64{}
	for _, pair := range strings.Split(raw, ",") {
		name, value, found := strings.Cut(pair, "=")
		name = strings.ToLower(strings.TrimSpace(name))
		mbps, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if !found || name == "" || err != nil || mbps <= 0 {
			return nil, false
		}
		tiers[name] = mbps
	}
	return tiers, len(tiers) > 0
}
