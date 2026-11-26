package behaviour

import (
	"math"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/netmeter"
)

var now = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

// freeRef is the Free tier's reference rate in bytes per second: 10 Mbit/s.
const freeRef = 10 * 125_000.0

func testConfig() Config {
	cfg := DefaultConfig()
	cfg.ReferenceMbps = map[string]float64{"free": 10, "standard": 100}
	cfg.DefaultReferenceMbps = 100
	cfg.SymmetryFloorKBps, cfg.DailyFloorGB = nil, nil
	return cfg
}

// series is a window of minutes at a steady rate: tx as a share of the Free reference, rx as a multiple of tx.
func series(minutes int, txPercentOfRef, rxOverTx float64) []netmeter.Sample {
	out := make([]netmeter.Sample, minutes)
	for i := range out {
		tx := freeRef * txPercentOfRef / 100
		out[i] = netmeter.Sample{At: now.Add(time.Duration(i-minutes) * time.Minute), ProjectID: "p", AppID: "a", Instances: 1, TxBps: tx, RxBps: tx * rxOverTx}
	}
	return out
}

func app(tier string) Subject {
	return Subject{OwnerID: "u1", OrgID: "o1", ProjectID: "p", ProjectName: "shop", AppID: "a", AppName: "web", Tier: tier}
}

func signals(findings []Finding) map[string]Finding {
	out := map[string]Finding{}
	for _, f := range findings {
		out[f.Signal] = f
	}
	return out
}

func TestEvaluate_SustainedEgressNeedsEnoughHighMinutes(t *testing.T) {
	cfg := testConfig()
	high := series(60, 85, 0.1)
	if got := signals(Evaluate(cfg, app("free"), freeRef, Observed{Minutes: high, Totals: Totals{}})); got[SignalSustainedEgress].Signal == "" {
		t.Fatal("85% of the reference for the whole hour is sustained egress")
	}
	mixed := series(60, 85, 0.1)
	for i := 0; i < 20; i++ { // 40 high minutes of 60, under the 45 needed
		mixed[i].TxBps = 0
	}
	if got := signals(Evaluate(cfg, app("free"), freeRef, Observed{Minutes: mixed, Totals: Totals{}})); got[SignalSustainedEgress].Signal != "" {
		t.Fatal("40 high minutes of 60 is under the 45 needed")
	}
	exactly := series(60, 85, 0.1)
	for i := 0; i < 15; i++ { // exactly the 45 high minutes needed
		exactly[i].TxBps = 0
	}
	if got := signals(Evaluate(cfg, app("free"), freeRef, Observed{Minutes: exactly, Totals: Totals{}})); got[SignalSustainedEgress].Signal == "" {
		t.Fatal("45 high minutes of 60 is exactly the number needed")
	}
	below := series(60, 75, 0.1)
	if got := signals(Evaluate(cfg, app("free"), freeRef, Observed{Minutes: below, Totals: Totals{}})); got[SignalSustainedEgress].Signal != "" {
		t.Fatal("75% is under the 80% threshold")
	}
	short := series(30, 95, 0.1)
	if got := signals(Evaluate(cfg, app("free"), freeRef, Observed{Minutes: short, Totals: Totals{}})); got[SignalSustainedEgress].Signal != "" {
		t.Fatal("a window with fewer minutes than the 45 needed cannot be sustained")
	}
}

func TestEvaluate_SustainedEgressEvidenceIsNumbersAboutTheWindow(t *testing.T) {
	f := signals(Evaluate(testConfig(), app("free"), freeRef, Observed{Minutes: series(60, 90, 0.1), Totals: Totals{}}))[SignalSustainedEgress]
	if f.Magnitude != 90 || f.Evidence["minutesAbove"] != 60 || f.Evidence["windowMinutes"] != 60 ||
		f.Evidence["percentOfReference"] != 90 || f.Evidence["referenceBps"] != freeRef || f.Evidence["avgTxBps"] != freeRef*0.9 {
		t.Fatalf("finding = %+v", f)
	}
}

func TestEvaluate_RelaySymmetryFlagsAOneToOneRelay(t *testing.T) {
	cfg := testConfig()
	got := signals(Evaluate(cfg, app("free"), freeRef, Observed{Minutes: series(60, 60, 1.0), Totals: Totals{}}))
	f := got[SignalRelaySymmetry]
	if f.Signal == "" || f.Evidence["ratio"] != 1 || f.Magnitude != 60 {
		t.Fatalf("a 1:1 relay at 60%% of the reference: %+v", got)
	}
	if got[SignalSustainedEgress].Signal != "" {
		t.Fatal("60% is below the sustained egress threshold; symmetry is what caught it")
	}
}

func TestEvaluate_SymmetryBandEdges(t *testing.T) {
	cfg := testConfig()
	for ratio, want := range map[float64]bool{0.4: false, 0.5: true, 1.0: true, 2.0: true, 2.1: false, 20: false, 0.02: false} {
		got := signals(Evaluate(cfg, app("free"), freeRef, Observed{Minutes: series(60, 60, ratio), Totals: Totals{}}))[SignalRelaySymmetry].Signal != ""
		if got != want {
			t.Errorf("rx/tx %v: flagged=%v, want %v", ratio, got, want)
		}
	}
}

func TestEvaluate_SymmetryNeedsRealVolumeAndEnoughActiveMinutes(t *testing.T) {
	cfg := testConfig()
	if got := signals(Evaluate(cfg, app("free"), freeRef, Observed{Minutes: series(60, 10, 1.0), Totals: Totals{}})); got[SignalRelaySymmetry].Signal != "" {
		t.Fatal("a quiet 1:1 connection (a chat socket) is below the volume floor")
	}
	burst := series(60, 60, 1.0)
	for i := 0; i < 30; i++ {
		burst[i].TxBps, burst[i].RxBps = 0, 0
	}
	if got := signals(Evaluate(cfg, app("free"), freeRef, Observed{Minutes: burst, Totals: Totals{}})); got[SignalRelaySymmetry].Signal != "" {
		t.Fatal("30 active minutes of 60 is under the 45 needed")
	}
}

func TestEvaluate_VolumeSignalsUseTheReferenceOverADayAndThirtyDays(t *testing.T) {
	cfg := testConfig()
	day := freeRef * 86400
	got := signals(Evaluate(cfg, app("free"), freeRef, Observed{Minutes: nil, Totals: Totals{TxDay: day * 0.31, Tx30d: day * 30 * 0.05}}))
	if got[SignalVolumeDaily].Signal == "" || got[SignalVolumeDaily].Magnitude < 30.9 || got[SignalVolumeDaily].Magnitude > 31.1 {
		t.Fatalf("31%% of a day at the reference rate is over the 30%% line: %+v", got)
	}
	if got[SignalVolume30d].Signal != "" {
		t.Fatal("5% of 30 days is under the monthly line")
	}
	got = signals(Evaluate(cfg, app("free"), freeRef, Observed{Minutes: nil, Totals: Totals{TxDay: day * 0.29, Tx30d: day * 30 * 0.25}}))
	if got[SignalVolumeDaily].Signal != "" || got[SignalVolume30d].Signal == "" {
		t.Fatalf("29%% of a day is under, 25%% of 30 days is over: %+v", got)
	}
}

func TestEvaluate_ReferenceDecidesWhatIsHigh(t *testing.T) {
	cfg := testConfig()
	paidRef := 100 * 125_000.0
	fiftyMbit := series(60, 500, 0.1) // 50 Mbit/s, which is 500% of the Free reference
	if got := signals(Evaluate(cfg, app("free"), freeRef, Observed{Minutes: fiftyMbit, Totals: Totals{}})); got[SignalSustainedEgress].Signal == "" {
		t.Fatal("50 Mbit/s on a Free app is far over its reference")
	}
	if got := signals(Evaluate(cfg, app("standard"), paidRef, Observed{Minutes: fiftyMbit, Totals: Totals{}})); got[SignalSustainedEgress].Signal != "" {
		t.Fatal("50 Mbit/s on a 100 Mbit/s reference is half of it, not sustained egress")
	}
	if got := Evaluate(cfg, app("free"), 0, Observed{Minutes: fiftyMbit, Totals: Totals{TxDay: 1e15}}); len(got) != 0 {
		t.Fatal("no reference, no judgement")
	}
}

func TestEvaluate_FindingsCarryTheSubjectSnapshot(t *testing.T) {
	f := Evaluate(testConfig(), app("free"), freeRef, Observed{Minutes: series(60, 90, 0.1), Totals: Totals{}})[0]
	if f.OwnerID != "u1" || f.OrgID != "o1" || f.ProjectID != "p" || f.ProjectName != "shop" || f.AppID != "a" || f.AppName != "web" || f.Tier != "free" {
		t.Fatalf("subject = %+v", f.Subject)
	}
}

func TestEvidenceHoldsOnlyNumbers(t *testing.T) {
	for _, f := range Evaluate(testConfig(), app("free"), freeRef, Observed{Minutes: series(60, 90, 1.0), Totals: Totals{TxDay: freeRef * 86400, Tx30d: freeRef * 86400 * 30}}) {
		for key, value := range f.Evidence {
			if key == "" || math.IsNaN(value) { // NaN never serialises
				t.Errorf("%s evidence %q = %v", f.Signal, key, value)
			}
		}
		if !evidenceKeysAllowed(f.Evidence) {
			t.Errorf("%s evidence has a key outside the fixed set: %v", f.Signal, f.Evidence)
		}
	}
}
