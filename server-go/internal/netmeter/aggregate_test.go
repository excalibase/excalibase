package netmeter

import "testing"

func TestAggregate_SumsReplicasAndKeepsTheBusiestPod(t *testing.T) {
	rates := []Rate{
		{Key: "p1", ProjectID: "proj", AppID: "a", RxBps: 10, TxBps: 100},
		{Key: "p2", ProjectID: "proj", AppID: "a", RxBps: 20, TxBps: 300},
		{Key: "p3", ProjectID: "proj", AppID: "b", RxBps: 1, TxBps: 2},
	}
	samples := Aggregate(t0, rates)
	if len(samples) != 2 {
		t.Fatalf("one sample per app: %+v", samples)
	}
	a := samples[0]
	if a.AppID != "a" || a.Instances != 2 || a.RxBps != 30 || a.TxBps != 400 || a.MaxInstanceTxBps != 300 || !a.At.Equal(t0) {
		t.Errorf("app a = %+v", a)
	}
	if samples[1].AppID != "b" || samples[1].Instances != 1 {
		t.Errorf("app b = %+v", samples[1])
	}
}

func TestAggregate_SkipsRatesWithoutAnApp(t *testing.T) {
	samples := Aggregate(t0, []Rate{{Key: "x", ProjectID: "", AppID: "a"}, {Key: "y", ProjectID: "p", AppID: ""}})
	if len(samples) != 0 {
		t.Fatalf("a pod we cannot attribute is not stored: %+v", samples)
	}
}

func TestAggregate_OrderIsStable(t *testing.T) {
	rates := []Rate{{ProjectID: "z", AppID: "a"}, {ProjectID: "a", AppID: "z"}, {ProjectID: "a", AppID: "b"}}
	got := Aggregate(t0, rates)
	want := [][2]string{{"a", "b"}, {"a", "z"}, {"z", "a"}}
	for i, pair := range want {
		if got[i].ProjectID != pair[0] || got[i].AppID != pair[1] {
			t.Fatalf("order %d = %s/%s, want %s/%s", i, got[i].ProjectID, got[i].AppID, pair[0], pair[1])
		}
	}
}
