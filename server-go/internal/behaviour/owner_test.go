package behaviour

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/netmeter"
)

func appMinutes(app string, tx float64, rxOverTx float64) []netmeter.Sample {
	out := series(60, 0, 0)
	for i := range out {
		out[i].AppID, out[i].TxBps, out[i].RxBps = app, freeRef*tx/100, freeRef*tx/100*rxOverTx
	}
	return out
}

func TestSumByTimestamp_AddsAnOwnersAppsMinuteByMinute(t *testing.T) {
	total := SumByTimestamp([][]netmeter.Sample{appMinutes("a", 10, 1), appMinutes("b", 20, 2), appMinutes("c", 30, 0)})
	if len(total) != 60 || total[0].TxBps != freeRef*0.6 || total[0].RxBps != freeRef*(0.1+0.4) || total[0].Instances != 3 {
		t.Fatalf("total[0] = %+v of %d", total[0], len(total))
	}
	if !total[0].At.Before(total[59].At) {
		t.Fatal("oldest first")
	}
}

func TestSumByTimestamp_ReferenceOfAnOwnerIsItsHighestTierNotTheSum(t *testing.T) {
	if got := OwnerReference(map[string]float64{"free": 10, "standard": 100}, 100, []string{"free", "free", "standard"}); got != 100*125_000 {
		t.Fatalf("reference = %v: an account is judged against one allowance, so splitting across apps does not raise it", got)
	}
	if got := OwnerReference(map[string]float64{"free": 10}, 100, []string{"free", "free", "free"}); got != freeRef {
		t.Fatalf("five free apps share one Free allowance: %v", got)
	}
	if got := OwnerReference(map[string]float64{"free": 10}, 100, nil); got != 100*125_000 {
		t.Fatalf("no live app: the default reference, got %v", got)
	}
}

func TestEvaluate_SplittingIntoManyAppsStillTripsTheOwnerLevel(t *testing.T) {
	cfg := testConfig()
	var perApp [][]netmeter.Sample
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		minutes := appMinutes(id, 20, 1.0) // each app alone: 20% of the reference, below every line
		if got := signals(Evaluate(cfg, app("free"), freeRef, minutes, Totals{})); len(got) != 0 {
			t.Fatalf("app %s alone raises %+v", id, got)
		}
		perApp = append(perApp, minutes)
	}
	owner := Subject{OwnerID: "u1"}
	got := signals(Evaluate(cfg, owner, freeRef, SumByTimestamp(perApp), Totals{}))
	if got[SignalSustainedEgress].Signal == "" || got[SignalRelaySymmetry].Signal == "" {
		t.Fatalf("five apps together at 100%% of one Free allowance, 1:1: %+v", got)
	}
	if got[SignalSustainedEgress].AppID != "" {
		t.Fatal("an owner finding names no app")
	}
}
