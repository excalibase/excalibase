package behaviour

import (
	"sort"

	"github.com/excalibase/provisioning-poc/internal/netmeter"
)

// Signal names, stored with each review.
const (
	SignalSustainedEgress = "sustained_egress"
	SignalRelaySymmetry   = "relay_symmetry"
	SignalRelayDay        = "relay_symmetry_day"
	SignalVolumeDaily     = "volume_daily"
	SignalVolume30d       = "volume_30d"
)

const (
	secondsPerDay = 86400.0
	daysPerMonth  = 30.0
)

// Subject is who a finding is about, snapshotted when it is made so it
// outlives the app, the project and the account. AppID empty means the owner
// as a whole.
type Subject struct {
	OwnerID, OrgID, ProjectID, ProjectName, AppID, AppName, Tier string
}

// Finding is one signal raised for one subject.
type Finding struct {
	Subject
	Signal string
	// Magnitude is the one number that grows as the behaviour gets worse (a percentage of the reference).
	Magnitude float64
	// Evidence is numbers only: rates, ratios, counts. Never an address, a name or content.
	Evidence map[string]float64
}

// Totals are the bytes sent and received over the last 24 hours and the last 30 days.
type Totals struct{ TxDay, RxDay, Tx30d, Rx30d float64 }

// evidenceKeys is the fixed set of keys evidence may carry.
var evidenceKeys = map[string]bool{
	"windowMinutes": true, "minutesAbove": true, "activeMinutes": true, "avgTxBps": true, "avgRxBps": true,
	"percentOfReference": true, "referenceBps": true, "ratio": true, "txBytes": true, "rxBytes": true,
	"percentOfPeriodReference": true, "periodHours": true, "apps": true,
	"windowHours": true, "activeHours": true, "floorBps": true,
}

func evidenceKeysAllowed(evidence map[string]float64) bool {
	for key := range evidence {
		if !evidenceKeys[key] {
			return false
		}
	}
	return true
}

// ValidEvidence says whether every key is one of the fixed numeric ones.
func ValidEvidence(evidence map[string]float64) bool { return evidenceKeysAllowed(evidence) }

// Observed is what the meter saw of one subject: its recent minutes, its
// hours over the last day, and its byte totals over 24 hours and 30 days.
type Observed struct {
	Minutes []netmeter.Sample
	Hours   []netmeter.Hourly
	Totals  Totals
}

// Evaluate raises the findings for one subject: its recent minutes judged
// against reference (bytes per second), its hours against the absolute floors
// of its tier, and its volumes over 24 hours and 30 days.
func Evaluate(cfg Config, subject Subject, reference float64, seen Observed) []Finding {
	if reference <= 0 {
		return nil
	}
	totals := seen.Totals
	var findings []Finding
	for _, check := range []func() *Finding{
		func() *Finding { return sustainedEgress(cfg, subject, reference, seen.Minutes) },
		func() *Finding { return relaySymmetry(cfg, subject, reference, seen.Minutes) },
		func() *Finding { return relayDay(cfg, subject, seen.Hours) },
		func() *Finding {
			return volume(subject, reference, volumeWindow{
				signal: SignalVolumeDaily, hours: 24, linePercent: cfg.DailyVolumePercent, seconds: secondsPerDay,
				floorBytes: cfg.DailyFloorBytes(subject.Tier), tx: totals.TxDay, rx: totals.RxDay,
			})
		},
		func() *Finding {
			return volume(subject, reference, volumeWindow{
				signal: SignalVolume30d, hours: 24 * daysPerMonth, linePercent: cfg.MonthlyVolumePercent,
				seconds: secondsPerDay * daysPerMonth, tx: totals.Tx30d, rx: totals.Rx30d,
			})
		},
	} {
		if finding := check(); finding != nil {
			findings = append(findings, *finding)
		}
	}
	return findings
}

func mean(total float64, count int) float64 {
	if count == 0 {
		return 0
	}
	return total / float64(count)
}

func sustainedEgress(cfg Config, subject Subject, reference float64, minutes []netmeter.Sample) *Finding {
	threshold := cfg.SustainedPercent / 100 * reference
	var above int
	var tx, rx float64
	for _, minute := range minutes {
		if minute.TxBps >= threshold {
			above++
		}
		tx += minute.TxBps
		rx += minute.RxBps
	}
	if above < cfg.SustainedMinutes {
		return nil
	}
	avgTx := mean(tx, len(minutes))
	percent := avgTx / reference * 100
	return &Finding{Subject: subject, Signal: SignalSustainedEgress, Magnitude: percent, Evidence: map[string]float64{
		"windowMinutes": float64(len(minutes)), "minutesAbove": float64(above), "avgTxBps": avgTx,
		"avgRxBps": mean(rx, len(minutes)), "percentOfReference": percent, "referenceBps": reference,
	}}
}

func relaySymmetry(cfg Config, subject Subject, reference float64, minutes []netmeter.Sample) *Finding {
	floor := cfg.SymmetryMinPercent / 100 * reference
	var active int
	var tx, rx float64
	for _, minute := range minutes {
		if minute.TxBps >= floor {
			active++
			tx += minute.TxBps
			rx += minute.RxBps
		}
	}
	if active < cfg.SustainedMinutes || tx == 0 {
		return nil
	}
	ratio := rx / tx
	if ratio < cfg.SymmetryLow || ratio > cfg.SymmetryHigh {
		return nil
	}
	avgTx := mean(tx, active)
	percent := avgTx / reference * 100
	return &Finding{Subject: subject, Signal: SignalRelaySymmetry, Magnitude: percent, Evidence: map[string]float64{
		"windowMinutes": float64(len(minutes)), "activeMinutes": float64(active), "ratio": ratio, "avgTxBps": avgTx,
		"avgRxBps": mean(rx, active), "percentOfReference": percent, "referenceBps": reference,
	}}
}

// volumeWindow is one volume check: the bytes sent and received over a period,
// the share of the reference that is the line, and an absolute floor in bytes
// (0 for none) that flags the subject on its own when lower than that line.
type volumeWindow struct {
	signal      string
	hours       float64
	linePercent float64
	seconds     float64
	floorBytes  float64
	tx, rx      float64
}

// relayDay is the all-day relay signal: an absolute rate, not a share of the
// reference. A relay that stays far under the tier's line (the real incident
// moved under 1.5 GB in three days) still sends and receives about the same
// amount, hour after hour. Only hours at or above the tier's floor count, and
// the ratio is judged over those hours alone, so idle hours do not hide it.
func relayDay(cfg Config, subject Subject, hours []netmeter.Hourly) *Finding {
	floor := cfg.SymmetryFloorBps(subject.Tier)
	if floor <= 0 {
		return nil
	}
	var active int
	var tx, rx float64
	for _, hour := range hours {
		if hour.AvgTxBps >= floor {
			active++
			tx += hour.AvgTxBps
			rx += hour.AvgRxBps
		}
	}
	if active < cfg.SymmetryDayHours || tx == 0 {
		return nil
	}
	ratio := rx / tx
	if ratio < cfg.SymmetryLow || ratio > cfg.SymmetryHigh {
		return nil
	}
	avgTx := mean(tx, active)
	return &Finding{Subject: subject, Signal: SignalRelayDay, Magnitude: avgTx / floor * 100, Evidence: map[string]float64{
		"windowHours": float64(len(hours)), "activeHours": float64(active), "ratio": ratio,
		"avgTxBps": avgTx, "avgRxBps": mean(rx, active), "floorBps": floor,
	}}
}

func volume(subject Subject, reference float64, window volumeWindow) *Finding {
	full := reference * window.seconds
	percent := window.tx / full * 100
	line := full * window.linePercent / 100
	if window.floorBytes > 0 {
		line = min(line, window.floorBytes)
	}
	if window.tx < line {
		return nil
	}
	return &Finding{Subject: subject, Signal: window.signal, Magnitude: percent, Evidence: map[string]float64{
		"periodHours": window.hours, "txBytes": window.tx, "rxBytes": window.rx, "percentOfPeriodReference": percent, "referenceBps": reference,
	}}
}

// SumByTimestamp adds several apps' minutes together, minute by minute, oldest
// first: an owner's whole traffic, however many apps it is split across.
func SumByTimestamp(perApp [][]netmeter.Sample) []netmeter.Sample {
	byTime := map[int64]*netmeter.Sample{}
	for _, minutes := range perApp {
		for _, minute := range minutes {
			key := minute.At.Unix()
			total := byTime[key]
			if total == nil {
				total = &netmeter.Sample{At: minute.At}
				byTime[key] = total
			}
			total.Instances++
			total.RxBps += minute.RxBps
			total.TxBps += minute.TxBps
		}
	}
	out := make([]netmeter.Sample, 0, len(byTime))
	for _, total := range byTime {
		out = append(out, *total)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// SumHours adds several apps' hours together, hour by hour, oldest first.
func SumHours(perApp [][]netmeter.Hourly) []netmeter.Hourly {
	byHour := map[int64]*netmeter.Hourly{}
	for _, hours := range perApp {
		for _, hour := range hours {
			key := hour.Hour.Unix()
			total := byHour[key]
			if total == nil {
				total = &netmeter.Hourly{Hour: hour.Hour}
				byHour[key] = total
			}
			total.AvgRxBps += hour.AvgRxBps
			total.AvgTxBps += hour.AvgTxBps
			total.Samples += hour.Samples
		}
	}
	out := make([]netmeter.Hourly, 0, len(byHour))
	for _, total := range byHour {
		out = append(out, *total)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hour.Before(out[j].Hour) })
	return out
}
