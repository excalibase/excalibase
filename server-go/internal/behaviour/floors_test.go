package behaviour

import (
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/netmeter"
)

const kb = 1000.0

// hoursOf is 24 hourly rows ending at now; active says which hours move txBps/rxBps, the rest are idle.
func hoursOf(active func(hour int) bool, txBps, rxBps float64) []netmeter.Hourly {
	out := make([]netmeter.Hourly, 24)
	for i := range out {
		out[i] = netmeter.Hourly{Hour: now.Add(time.Duration(i-24) * time.Hour).Truncate(time.Hour), ProjectID: "p", AppID: "a", Samples: 60}
		if active(i) {
			out[i].AvgTxBps, out[i].AvgRxBps = txBps, rxBps
		}
	}
	return out
}

func firstHours(n int) func(int) bool { return func(i int) bool { return i < n } }

func always(int) bool { return true }

func bytesOf(hours []netmeter.Hourly) float64 {
	var total float64
	for _, h := range hours {
		total += (h.AvgTxBps + h.AvgRxBps) * 3600
	}
	return total
}

// The incident, from the real figures: one pod moved under 1.5 GB in about three
// days. That is 0.5 GB a day, a fraction of a percent of a 10 Mbit/s reference, so
// nothing scaled to the reference can see it. A symmetric relay that holds about
// 10 KB/s each way for six hours of a day is what those totals look like.
func TestEvaluate_ABurstyRelayAtTheIncidentsVolumeIsFlaggedByTheAbsoluteFloors(t *testing.T) {
	cfg := DefaultConfig()
	day := hoursOf(firstHours(6), 10.5*kb, 10.5*kb)
	if three := 3 * bytesOf(day); three >= 1.5e9 {
		t.Fatalf("the model moves %.2f GB in three days, over the %.1f GB seen", three/1e9, 1.5)
	}
	got := signals(Evaluate(cfg, app("free"), freeRef, Observed{Minutes: series(60, 0.9, 1.0), Hours: day}))
	f := got[SignalRelayDay]
	if f.Signal == "" || f.Evidence["activeHours"] != 6 || f.Evidence["ratio"] != 1 || f.Evidence["floorBps"] != 10*kb {
		t.Fatalf("a 1:1 relay at 10.5 KB/s for 6 hours of the day: %+v", got)
	}
	if got[SignalRelaySymmetry].Signal != "" || got[SignalSustainedEgress].Signal != "" || got[SignalVolumeDaily].Signal != "" {
		t.Fatalf("only the all-day floor sees this volume, not the reference-scaled signals: %+v", got)
	}
}

// A steady 2 Mbit/s each way is 20% of the Free reference: under the 25% the
// short-window symmetry needs, under the 80% sustained line, and still 21 GB a day.
func TestEvaluate_ASteadyTwoMbitSymmetricRelayIsFlagged(t *testing.T) {
	cfg := DefaultConfig()
	twoMbit := 250 * kb
	got := signals(Evaluate(cfg, app("free"), freeRef, Observed{
		Minutes: series(60, 20, 1.0),
		Hours:   hoursOf(always, twoMbit, twoMbit),
		Totals:  Totals{TxDay: twoMbit * 86400, RxDay: twoMbit * 86400},
	}))
	if got[SignalRelayDay].Signal == "" || got[SignalVolumeDaily].Signal == "" {
		t.Fatalf("2 Mbit/s each way all day: %+v", got)
	}
	if got[SignalRelayDay].Evidence["activeHours"] != 24 {
		t.Fatalf("active hours = %v", got[SignalRelayDay].Evidence["activeHours"])
	}
}

func TestEvaluate_AllDayRelayEdges(t *testing.T) {
	cfg := DefaultConfig()
	flagged := func(hours []netmeter.Hourly, tier string) bool {
		return signals(Evaluate(cfg, app(tier), cfg.Reference(tier), Observed{Hours: hours}))[SignalRelayDay].Signal != ""
	}
	if !flagged(hoursOf(firstHours(6), 10*kb, 10*kb), "free") {
		t.Error("exactly 10 KB/s for exactly 6 hours is on the line")
	}
	if flagged(hoursOf(firstHours(6), 9.9*kb, 9.9*kb), "free") {
		t.Error("9.9 KB/s is under the floor")
	}
	if flagged(hoursOf(firstHours(5), 10*kb, 10*kb), "free") {
		t.Error("5 hours is under the 6 needed")
	}
	if flagged(hoursOf(firstHours(6), 10*kb, 10*kb), "standard") {
		t.Error("a tier with no floor configured has no all-day relay signal")
	}
	for ratio, want := range map[float64]bool{0.4: false, 0.5: true, 2.0: true, 2.1: false} {
		if got := flagged(hoursOf(always, 10*kb, 10*kb*ratio), "free"); got != want {
			t.Errorf("rx/tx %v: flagged=%v, want %v", ratio, got, want)
		}
	}
	// Idle hours do not dilute the ratio: only the hours at the floor are judged.
	mixed := hoursOf(firstHours(6), 10*kb, 10*kb)
	for i := 6; i < 24; i++ {
		mixed[i].AvgTxBps, mixed[i].AvgRxBps = 1*kb, 200*kb // a quiet download in the other hours
	}
	if !flagged(mixed, "free") {
		t.Error("the hours below the floor are not part of the ratio")
	}
}

func TestEvaluate_DailyFloorFlagsAFewGigabytesOnFreeButNotOnATierWithoutOne(t *testing.T) {
	cfg := DefaultConfig()
	daily := func(tier string, tx float64) bool {
		return signals(Evaluate(cfg, app(tier), cfg.Reference(tier), Observed{Totals: Totals{TxDay: tx}}))[SignalVolumeDaily].Signal != ""
	}
	if daily("free", 2.99e9) || !daily("free", 3e9) {
		t.Error("Free's daily floor is 3 GB: under is quiet, on the line is reviewed")
	}
	if daily("standard", 50e9) {
		t.Error("Standard has no floor, and 50 GB is under 30% of its reference day")
	}
	cfg.DailyFloorGB["free"] = 1
	if !daily("free", 1.5e9) {
		t.Error("the floor comes from config")
	}
}

// The false-positive set against the real defaults, floors included.
func TestEvaluate_OrdinaryAppsStayQuietUnderTheDefaultFloors(t *testing.T) {
	cfg := DefaultConfig()
	cases := map[string]Observed{
		"a chat app: 1:1 but 1 KB/s each way all day, 86 MB each way": {
			Minutes: series(60, 0.08, 1), Hours: hoursOf(always, 1*kb, 1*kb), Totals: Totals{TxDay: 86e6, RxDay: 86e6}},
		"a small API: 3 KB/s out, 0.5 KB/s in, all day": {
			Minutes: series(60, 0.24, 0.17), Hours: hoursOf(always, 3*kb, 0.5*kb), Totals: Totals{TxDay: 260e6, RxDay: 43e6}},
		"a busy hour of a business API: 200 KB/s out, 20 KB/s in, two hours": {
			Minutes: series(60, 16, 0.1), Hours: hoursOf(firstHours(2), 200*kb, 20*kb), Totals: Totals{TxDay: 1.4e9, RxDay: 144e6}},
		"a webhook receiver: rx heavy, 30 KB/s in for 10 hours": {
			Hours: hoursOf(firstHours(10), 2*kb, 30*kb), Totals: Totals{TxDay: 72e6, RxDay: 1.1e9}},
		"an idle app": {Minutes: series(60, 0, 0), Hours: hoursOf(func(int) bool { return false }, 0, 0)},
	}
	for name, seen := range cases {
		if got := Evaluate(cfg, app("free"), freeRef, seen); len(got) != 0 {
			t.Errorf("%s was flagged: %+v", name, got)
		}
	}
}

// The known miss, pinned: the incident's volume (about 0.45 GB a day each way
// over three days) spread evenly over the whole day is under every floor, so
// nothing is raised. The connection signals are what catch it.
func TestEvaluate_TheIncidentsVolumeSpreadEvenlyAllDayRaisesNothing(t *testing.T) {
	cfg := DefaultConfig()
	perSecond := 2.6 * kb // 2.6 KB/s each way is 225 MB a day each way
	hours := hoursOf(always, perSecond, perSecond)
	if three := 3 * bytesOf(hours); three >= 1.5e9 {
		t.Fatalf("the model moves %.2f GB in three days", three/1e9)
	}
	seen := Observed{Minutes: series(60, 0.2, 1.0), Hours: hours, Totals: Totals{TxDay: perSecond * 86400, RxDay: perSecond * 86400}}
	if got := Evaluate(cfg, app("free"), freeRef, seen); len(got) != 0 {
		t.Fatalf("an evenly spread relay at this volume is under the floor and is not seen: %+v", got)
	}
}

// The main expected noise, pinned: an app that reads its own database and
// returns the data looks symmetric, because database traffic counts on the pod's
// network. A thin API or SSR site at this rate is flagged until the edge signal
// (external traffic only) can tell the two apart.
func TestEvaluate_ADatabaseBackedAppAtFifteenKBpsIsAKnownFalsePositive(t *testing.T) {
	got := signals(Evaluate(DefaultConfig(), app("free"), freeRef, Observed{Hours: hoursOf(firstHours(8), 15*kb, 14*kb)}))
	if got[SignalRelayDay].Signal == "" {
		t.Fatalf("15 KB/s out and 14 in for 8 hours raises relay_symmetry_day: %+v", got)
	}
}
