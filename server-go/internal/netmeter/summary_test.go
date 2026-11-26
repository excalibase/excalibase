package netmeter

import (
	"strings"
	"testing"
	"time"
)

const summaryJSON = `{"node":{"nodeName":"n1"},"pods":[
 {"podRef":{"name":"app-web-1","namespace":"ns1","uid":"uid-1"},"startTime":"2026-11-26T09:00:00Z",
  "network":{"time":"2026-11-26T10:00:00Z","name":"eth0","rxBytes":1000,"txBytes":2000,
   "interfaces":[{"name":"eth0","rxBytes":1000,"txBytes":2000}]}},
 {"podRef":{"name":"hostnet","namespace":"kube-system","uid":"uid-2"},"startTime":"2026-11-01T09:00:00Z"},
 {"podRef":{"name":"app-web-2","namespace":"ns1","uid":"uid-3"},"startTime":"2026-11-26T09:30:00Z",
  "network":{"time":"2026-11-26T10:00:05Z","name":"eth0","rxBytes":5,"txBytes":6}}
]}`

func TestParseSummary_ReadsPodNetworkCounters(t *testing.T) {
	pods, err := ParseSummary(strings.NewReader(summaryJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(pods) != 2 {
		t.Fatalf("a pod without network counters is skipped: %+v", pods)
	}
	first := pods[0]
	if first.UID != "uid-1" || first.Namespace != "ns1" || first.Name != "app-web-1" || first.Rx != 1000 || first.Tx != 2000 {
		t.Errorf("first = %+v", first)
	}
	if !first.At.Equal(t0) || !first.Started.Equal(t0.Add(-time.Hour)) {
		t.Errorf("the counters' own time and the pod's start: %+v", first)
	}
}

func TestParseSummary_RejectsBrokenJSON(t *testing.T) {
	if _, err := ParseSummary(strings.NewReader(`{"pods":[`)); err == nil {
		t.Fatal("a broken answer is an error, not an empty list")
	}
}

func TestParseSummary_RefusesAnOversizedAnswer(t *testing.T) {
	huge := `{"pods":[]}` + strings.Repeat(" ", maxSummaryBytes+1)
	if _, err := ParseSummary(strings.NewReader(huge)); err == nil {
		t.Fatal("an answer past the limit is refused")
	}
}
