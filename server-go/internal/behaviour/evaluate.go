package behaviour

import (
	"sort"

	"github.com/excalibase/provisioning-poc/internal/netmeter"
)

// Signal names, stored with each review.
const (
	SignalSustainedEgress = "sustained_egress"
	SignalRelaySymmetry   = "relay_symmetry"
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

// Evaluate raises the findings for one subject: its recent minutes judged
// against reference (bytes per second) and its volumes over 24 hours and 30 days.
func Evaluate(cfg Config, subject Subject, reference float64, minutes []netmeter.Sample, totals Totals) []Finding {
	if reference <= 0 {
		return nil
	}
	var findings []Finding
	for _, check := range []func() *Finding{
		func() *Finding { return sustainedEgress(cfg, subject, reference, minutes) },
		func() *Finding { return relaySymmetry(cfg, subject, reference, minutes) },
		func() *Finding {
			return volume(cfg, subject, reference, SignalVolumeDaily, 24, cfg.DailyVolumePercent, secondsPerDay, totals.TxDay, totals.RxDay)
		},
		func() *Finding {
			return volume(cfg, subject, reference, SignalVolume30d, 24*daysPerMonth, cfg.MonthlyVolumePercent, secondsPerDay*daysPerMonth, totals.Tx30d, totals.Rx30d)
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

func volume(cfg Config, subject Subject, reference float64, signal string, hours, linePercent, seconds, tx, rx float64) *Finding {
	full := reference * seconds
	percent := tx / full * 100
	if percent < linePercent {
		return nil
	}
	return &Finding{Subject: subject, Signal: signal, Magnitude: percent, Evidence: map[string]float64{
		"periodHours": hours, "txBytes": tx, "rxBytes": rx, "percentOfPeriodReference": percent, "referenceBps": reference,
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

// OwnerReference is what an owner as a whole is judged against: its highest
// tier's reference, not the sum, so splitting a workload over many apps does
// not buy more allowance. With no live app it is the default.
func OwnerReference(referenceMbps map[string]float64, defaultMbps float64, tiers []string) float64 {
	cfg := Config{ReferenceMbps: referenceMbps, DefaultReferenceMbps: defaultMbps}
	best := 0.0
	for _, tier := range tiers {
		best = max(best, cfg.Reference(tier))
	}
	if best == 0 {
		return defaultMbps * bytesPerMbit
	}
	return best
}
