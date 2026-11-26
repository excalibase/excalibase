package behaviour

import (
	"strings"
	"time"
)

// bytesPerMbit converts a rate in Mbit/s to bytes per second.
const bytesPerMbit = 125_000.0

// Config holds every threshold. None is a constant of the code: each comes
// from the environment through the config package, and these are the defaults.
type Config struct {
	// WindowMinutes is how many recent minutes the sustained and symmetry signals look at.
	WindowMinutes int
	// SustainedPercent is the share of the tier's reference rate that counts as high egress.
	SustainedPercent float64
	// SustainedMinutes is how many minutes of the window must be high (or active, for symmetry).
	SustainedMinutes int
	// SymmetryLow and SymmetryHigh bound the rx/tx ratio of a relay: it hands on about as much as it takes in.
	SymmetryLow, SymmetryHigh float64
	// SymmetryMinPercent is the least tx, as a share of the reference rate, a minute needs to count as active.
	SymmetryMinPercent float64
	// DailyVolumePercent and MonthlyVolumePercent are tx over 24 hours and over 30 days as a share of what the
	// reference rate would move in that time.
	DailyVolumePercent, MonthlyVolumePercent float64
	// SymmetryFloorKBps is, by tier, the least tx in KB/s an hour needs to count toward the all-day relay signal.
	// It is an absolute rate that does not scale with the reference, so a relay that stays far below the tier's
	// line is still seen. A tier not named has no all-day relay signal.
	SymmetryFloorKBps map[string]float64
	// SymmetryDayHours is how many of the last 24 hours must hold that rate, in the relay band, to be flagged.
	SymmetryDayHours int
	// DailyFloorGB is, by tier, the tx in GB over 24 hours that is reviewed on its own, however low the reference
	// line would put it. A tier not named has no floor.
	DailyFloorGB map[string]float64
	// ReferenceMbps is each tier's reference rate by lower-case tier name; DefaultReferenceMbps covers a tier not named.
	// An uncapped tier's reference is its own baseline, set here.
	ReferenceMbps        map[string]float64
	DefaultReferenceMbps float64
	// EvalEvery is how often the detector looks.
	EvalEvery time.Duration
	// MuteDays is how long a dismissal silences a finding, unless it grows GrowthFactor times past the dismissed peak.
	MuteDays     int
	GrowthFactor float64
	// DismissedRetentionDays is how long dismissed reviews are kept.
	DismissedRetentionDays int
}

// DefaultConfig is what an install gets without any setting.
func DefaultConfig() Config {
	return Config{
		WindowMinutes: 60, SustainedPercent: 80, SustainedMinutes: 45,
		SymmetryLow: 0.5, SymmetryHigh: 2.0, SymmetryMinPercent: 25,
		SymmetryFloorKBps: map[string]float64{"free": 10}, SymmetryDayHours: 6, DailyFloorGB: map[string]float64{"free": 3},
		DailyVolumePercent: 30, MonthlyVolumePercent: 20,
		ReferenceMbps:        map[string]float64{"free": 10, "standard": 100, "enterprise": 1000},
		DefaultReferenceMbps: 100,
		EvalEvery:            5 * time.Minute, MuteDays: 14, GrowthFactor: 2, DismissedRetentionDays: 180,
	}
}

// Reference is a tier's reference rate in bytes per second.
func (c Config) Reference(tier string) float64 {
	mbps, ok := c.ReferenceMbps[strings.ToLower(tier)]
	if !ok {
		mbps = c.DefaultReferenceMbps
	}
	return mbps * bytesPerMbit
}

// SymmetryFloorBps is a tier's absolute all-day relay rate in bytes per second; 0 when the tier has none.
func (c Config) SymmetryFloorBps(tier string) float64 {
	return c.SymmetryFloorKBps[strings.ToLower(tier)] * 1000
}

// DailyFloorBytes is a tier's absolute daily tx floor in bytes; 0 when the tier has none.
func (c Config) DailyFloorBytes(tier string) float64 {
	return c.DailyFloorGB[strings.ToLower(tier)] * 1e9
}
