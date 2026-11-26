package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/behaviour"
	"github.com/excalibase/provisioning-poc/internal/features"
)

// behaviourEnvNames are the variables behaviour review reads; each overrides one default.
var behaviourEnvNames = []string{
	"BEHAVIOUR_WINDOW_MINUTES", "BEHAVIOUR_SUSTAINED_PERCENT", "BEHAVIOUR_SUSTAINED_MINUTES",
	"BEHAVIOUR_SYMMETRY_LOW", "BEHAVIOUR_SYMMETRY_HIGH", "BEHAVIOUR_SYMMETRY_MIN_PERCENT",
	"BEHAVIOUR_DAILY_VOLUME_PERCENT", "BEHAVIOUR_MONTHLY_VOLUME_PERCENT",
	"BEHAVIOUR_TIER_MBPS", "BEHAVIOUR_DEFAULT_MBPS", "BEHAVIOUR_EVAL_MINUTES",
	"BEHAVIOUR_MUTE_DAYS", "BEHAVIOUR_GROWTH_FACTOR", "BEHAVIOUR_DISMISSED_RETENTION_DAYS",
	"BEHAVIOUR_SYMMETRY_FLOOR_KBPS", "BEHAVIOUR_SYMMETRY_DAY_HOURS", "BEHAVIOUR_DAILY_FLOOR_GB",
}

// behaviourSettings reads the variables into a Config, collecting every
// mistake instead of correcting it: a value that is set but not usable is an
// error naming the variable. Nothing falls back to a default, because a
// threshold the operator did not mean is worse than a stopped boot.
type behaviourSettings struct {
	cfg  behaviour.Config
	errs []error
}

func (b *behaviourSettings) fail(name, raw, want string) {
	b.errs = append(b.errs, fmt.Errorf("%s=%q is not %s", name, raw, want))
}

// loadBehaviour starts from the defaults and applies each variable that is
// set. The error is non-nil when any is set to something unusable; Validate
// reports it while the flag is on, and the flag off ignores it.
func loadBehaviour() (behaviour.Config, error) {
	b := &behaviourSettings{cfg: behaviour.DefaultConfig()}
	cfg := &b.cfg
	b.wholeNumber(&cfg.WindowMinutes, "BEHAVIOUR_WINDOW_MINUTES")
	b.number(&cfg.SustainedPercent, "BEHAVIOUR_SUSTAINED_PERCENT")
	b.wholeNumber(&cfg.SustainedMinutes, "BEHAVIOUR_SUSTAINED_MINUTES")
	b.number(&cfg.SymmetryLow, "BEHAVIOUR_SYMMETRY_LOW")
	b.number(&cfg.SymmetryHigh, "BEHAVIOUR_SYMMETRY_HIGH")
	b.number(&cfg.SymmetryMinPercent, "BEHAVIOUR_SYMMETRY_MIN_PERCENT")
	b.number(&cfg.DailyVolumePercent, "BEHAVIOUR_DAILY_VOLUME_PERCENT")
	b.number(&cfg.MonthlyVolumePercent, "BEHAVIOUR_MONTHLY_VOLUME_PERCENT")
	b.number(&cfg.DefaultReferenceMbps, "BEHAVIOUR_DEFAULT_MBPS")
	b.wholeNumber(&cfg.MuteDays, "BEHAVIOUR_MUTE_DAYS")
	b.wholeNumber(&cfg.DismissedRetentionDays, "BEHAVIOUR_DISMISSED_RETENTION_DAYS")
	b.wholeNumber(&cfg.SymmetryDayHours, "BEHAVIOUR_SYMMETRY_DAY_HOURS")
	b.number(&cfg.GrowthFactor, "BEHAVIOUR_GROWTH_FACTOR")
	var evalMinutes int
	if b.wholeNumber(&evalMinutes, "BEHAVIOUR_EVAL_MINUTES") {
		cfg.EvalEvery = time.Duration(evalMinutes) * time.Minute
	}
	b.tierValues(&cfg.ReferenceMbps, "BEHAVIOUR_TIER_MBPS", false)
	b.tierValues(&cfg.SymmetryFloorKBps, "BEHAVIOUR_SYMMETRY_FLOOR_KBPS", true)
	b.tierValues(&cfg.DailyFloorGB, "BEHAVIOUR_DAILY_FLOOR_GB", true)
	b.coherent()
	return b.cfg, errors.Join(b.errs...)
}

// coherent checks the settings against each other.
func (b *behaviourSettings) coherent() {
	cfg := b.cfg
	if cfg.SymmetryLow >= cfg.SymmetryHigh {
		b.errs = append(b.errs, fmt.Errorf("BEHAVIOUR_SYMMETRY_LOW=%v and BEHAVIOUR_SYMMETRY_HIGH=%v leave an empty band", cfg.SymmetryLow, cfg.SymmetryHigh))
	}
	if cfg.SustainedMinutes > cfg.WindowMinutes {
		b.errs = append(b.errs, fmt.Errorf("BEHAVIOUR_SUSTAINED_MINUTES=%d is more than the %d-minute BEHAVIOUR_WINDOW_MINUTES, so the signal could never fire",
			cfg.SustainedMinutes, cfg.WindowMinutes))
	}
	if cfg.SymmetryDayHours > 24 {
		b.errs = append(b.errs, fmt.Errorf("BEHAVIOUR_SYMMETRY_DAY_HOURS=%d is more than the 24 hours in a day", cfg.SymmetryDayHours))
	}
	if cfg.GrowthFactor < 1 {
		b.errs = append(b.errs, fmt.Errorf("BEHAVIOUR_GROWTH_FACTOR=%v is under 1 and would end every mute at once", cfg.GrowthFactor))
	}
}

// wholeNumber sets *target from the variable when it is set; set but not a whole number above zero is an error.
func (b *behaviourSettings) wholeNumber(target *int, name string) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return false
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		b.fail(name, raw, "a whole number above zero")
		return false
	}
	*target = value
	return true
}

func (b *behaviourSettings) number(target *float64, name string) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return false
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value <= 0 {
		b.fail(name, raw, "a number above zero")
		return false
	}
	*target = value
	return true
}

// tierValues sets *target from "free=10,standard=100" when the variable is set;
// any malformed pair refuses the whole value. A zero is allowed only where it
// means "no floor for this tier".
func (b *behaviourSettings) tierValues(target *map[string]float64, name string, allowZero bool) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return
	}
	values, ok := parseTierValues(raw, allowZero)
	if !ok {
		b.fail(name, raw, "comma-separated tier=number pairs")
		return
	}
	*target = values
}

// parseTierValues reads "free=10,standard=100": a number per tier.
func parseTierValues(raw string, allowZero bool) (map[string]float64, bool) {
	values := map[string]float64{}
	for _, pair := range strings.Split(raw, ",") {
		name, value, found := strings.Cut(pair, "=")
		name = strings.ToLower(strings.TrimSpace(name))
		number, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if !found || name == "" || err != nil || number < 0 || (number == 0 && !allowZero) {
			return nil, false
		}
		values[name] = number
	}
	return values, len(values) > 0
}

// validateBehaviour stops the boot on a bad BEHAVIOUR_* setting while behaviour
// review is on. With the flag off the settings are not read, so a mistake in
// them is not this boot's business.
func (c AppConfig) validateBehaviour() error {
	if c.BehaviourErr == nil || !c.Features().Enabled(context.Background(), features.BehaviourReview) {
		return nil
	}
	return fmt.Errorf("behaviour review: %w", c.BehaviourErr)
}
