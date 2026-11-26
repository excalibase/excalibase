package netmeter

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)

func reading(key string, at time.Time, started time.Time, rx, tx uint64) Reading {
	return Reading{Key: key, ProjectID: "proj", AppID: "app", At: at, Started: started, Rx: rx, Tx: tx}
}

func onlyRate(t *testing.T, rates []Rate) Rate {
	t.Helper()
	if len(rates) != 1 {
		t.Fatalf("want one rate, got %d: %+v", len(rates), rates)
	}
	return rates[0]
}

func TestTracker_FirstObserveOnlyRecordsBaselines(t *testing.T) {
	tracker := NewTracker()
	if rates := tracker.Observe([]Reading{reading("a", t0, t0.Add(-time.Hour), 1000, 2000)}); len(rates) != 0 {
		t.Fatalf("the first observation after a start or leader change gives no rate, got %+v", rates)
	}
}

func TestTracker_RateComesFromTheReadingsOwnTime(t *testing.T) {
	tracker := NewTracker()
	tracker.Observe([]Reading{reading("a", t0, time.Time{}, 1000, 2000)})
	rate := onlyRate(t, tracker.Observe([]Reading{reading("a", t0.Add(30*time.Second), time.Time{}, 4000, 5000)}))
	if rate.RxBps != 100 || rate.TxBps != 100 {
		t.Errorf("3000 bytes over 30 s is 100 B/s, got rx %v tx %v", rate.RxBps, rate.TxBps)
	}
	if rate.ProjectID != "proj" || rate.AppID != "app" {
		t.Errorf("the rate keeps its app: %+v", rate)
	}
}

func TestTracker_CounterResetCountsTheNewValue(t *testing.T) {
	tracker := NewTracker()
	tracker.Observe([]Reading{reading("a", t0, time.Time{}, 9000, 9000)})
	rate := onlyRate(t, tracker.Observe([]Reading{reading("a", t0.Add(10*time.Second), time.Time{}, 500, 100)}))
	if rate.RxBps != 50 || rate.TxBps != 10 {
		t.Errorf("after a reset the delta is the new value: rx %v tx %v", rate.RxBps, rate.TxBps)
	}
}

func TestTracker_ResetIsJudgedPerDirection(t *testing.T) {
	tracker := NewTracker()
	tracker.Observe([]Reading{reading("a", t0, time.Time{}, 1000, 9000)})
	rate := onlyRate(t, tracker.Observe([]Reading{reading("a", t0.Add(10*time.Second), time.Time{}, 2000, 100)}))
	if rate.RxBps != 100 || rate.TxBps != 10 {
		t.Errorf("only tx reset: rx %v tx %v", rate.RxBps, rate.TxBps)
	}
}

func TestTracker_NewPodIsJudgedByItsStartTime(t *testing.T) {
	tracker := NewTracker()
	tracker.Observe([]Reading{reading("old", t0, time.Time{}, 0, 0)})
	started := t0.Add(60 * time.Second)
	rates := tracker.Observe([]Reading{
		reading("old", t0.Add(120*time.Second), time.Time{}, 0, 0),
		reading("new", t0.Add(120*time.Second), started, 6000, 12000),
	})
	var fresh *Rate
	for i := range rates {
		if rates[i].Key == "new" {
			fresh = &rates[i]
		}
	}
	if fresh == nil || fresh.RxBps != 100 || fresh.TxBps != 200 {
		t.Fatalf("a pod first seen 60 s after it started: bytes since start over 60 s, got %+v", fresh)
	}
}

func TestTracker_NewPodWithoutStartTimeGivesBaselineOnly(t *testing.T) {
	tracker := NewTracker()
	tracker.Observe([]Reading{reading("old", t0, time.Time{}, 0, 0)})
	rates := tracker.Observe([]Reading{reading("new", t0.Add(60*time.Second), time.Time{}, 6000, 6000)})
	if len(rates) != 0 {
		t.Fatalf("no start time, no rate: %+v", rates)
	}
	rate := onlyRate(t, tracker.Observe([]Reading{reading("new", t0.Add(120*time.Second), time.Time{}, 12000, 6000)}))
	if rate.RxBps != 100 || rate.TxBps != 0 {
		t.Errorf("the next sample has a baseline: %+v", rate)
	}
}

func TestTracker_StaleOrRepeatedReadingGivesNoRateAndKeepsTheBaseline(t *testing.T) {
	tracker := NewTracker()
	tracker.Observe([]Reading{reading("a", t0, time.Time{}, 1000, 1000)})
	if rates := tracker.Observe([]Reading{reading("a", t0, time.Time{}, 1000, 1000)}); len(rates) != 0 {
		t.Fatalf("the same kubelet time twice: %+v", rates)
	}
	if rates := tracker.Observe([]Reading{reading("a", t0.Add(500*time.Millisecond), time.Time{}, 2000, 2000)}); len(rates) != 0 {
		t.Fatalf("under a second apart is too short to divide by: %+v", rates)
	}
	rate := onlyRate(t, tracker.Observe([]Reading{reading("a", t0.Add(10*time.Second), time.Time{}, 2000, 2000)}))
	if rate.RxBps != 100 {
		t.Errorf("the baseline stayed at t0: %+v", rate)
	}
}

func TestTracker_AGonePodLosesItsBaselineAndComesBackAsNew(t *testing.T) {
	tracker := NewTracker()
	tracker.Observe([]Reading{reading("a", t0, time.Time{}, 100, 100), reading("b", t0, time.Time{}, 100, 100)})
	tracker.Observe([]Reading{reading("b", t0.Add(10*time.Second), time.Time{}, 200, 200)})
	rates := tracker.Observe([]Reading{reading("a", t0.Add(20*time.Second), time.Time{}, 300, 300)})
	if len(rates) != 0 {
		t.Fatalf("a pod that was missing a round has no baseline: %+v", rates)
	}
}

func TestTracker_ResetForgetsEverything(t *testing.T) {
	tracker := NewTracker()
	tracker.Observe([]Reading{reading("a", t0, time.Time{}, 0, 0)})
	tracker.Reset()
	started := t0.Add(-time.Hour) // judged by start time, a primed tracker would give a rate here
	if rates := tracker.Observe([]Reading{reading("a", t0.Add(10*time.Second), started, 1000, 1000)}); len(rates) != 0 {
		t.Fatalf("after Reset the next observation is a baseline again: %+v", rates)
	}
}
