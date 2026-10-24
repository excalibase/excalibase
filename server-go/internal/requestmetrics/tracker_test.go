package requestmetrics

import (
	"testing"
	"time"
)

var start = time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)

func reading(instance string, ok float64, opened, messages, connections float64) Reading {
	buckets := make([]float64, BucketCount)
	buckets[0] = ok
	for i := range buckets {
		buckets[i] = ok
	}
	return Reading{
		Instance:   instance,
		Histograms: map[SeriesKey][]float64{{Project: "p1", Family: "graphql", Outcome: "ok"}: buckets},
		Realtime:   map[string]Realtime{"p1": {Connections: connections, Opened: opened, Messages: messages}},
	}
}

func graphql(usage map[Key]Usage) Usage { return usage[Key{Project: "p1", Family: "graphql"}] }
func realtime(usage map[Key]Usage) Usage {
	return usage[Key{Project: "p1", Family: FamilyRealtime}]
}

func TestTracker_TheFirstScrapeIsOnlyABaseline(t *testing.T) {
	tracker := NewTracker()
	usage := tracker.Advance(start, []Reading{reading("a", 10, 1, 5, 2)})
	if graphql(usage).Requests != 0 {
		t.Errorf("counts before the first scrape are unknown in time: %+v", usage)
	}
	if realtime(usage).Connections != 2 {
		t.Errorf("open connections are a reading, not a delta: %+v", realtime(usage))
	}
}

func TestTracker_CountsTheDeltaSinceThePreviousScrape(t *testing.T) {
	tracker := NewTracker()
	tracker.Advance(start, []Reading{reading("a", 10, 1, 5, 2)})
	usage := tracker.Advance(start.Add(time.Minute), []Reading{reading("a", 14, 3, 9, 1)})
	if got := graphql(usage); got.Requests != 4 || got.Buckets[0] != 4 {
		t.Errorf("graphql delta: %+v", got)
	}
	if got := realtime(usage); got.Opened != 2 || got.Messages != 4 || got.Connections != 1 {
		t.Errorf("realtime delta: %+v", got)
	}
}

func TestTracker_SumsEveryReplica(t *testing.T) {
	tracker := NewTracker()
	tracker.Advance(start, []Reading{reading("a", 10, 0, 0, 1), reading("b", 100, 0, 0, 2)})
	usage := tracker.Advance(start.Add(time.Minute), []Reading{reading("a", 12, 0, 0, 1), reading("b", 105, 0, 0, 2)})
	if got := graphql(usage); got.Requests != 7 {
		t.Errorf("replicas summed: %+v", got)
	}
	if got := realtime(usage); got.Connections != 3 {
		t.Errorf("open connections summed: %+v", got)
	}
}

func TestTracker_ARestartedPodCountsEverythingItHasSinceItStarted(t *testing.T) {
	tracker := NewTracker()
	tracker.Advance(start, []Reading{reading("a", 500, 0, 0, 0)})
	// Pod a restarted as b between scrapes: its counters began again from zero.
	usage := tracker.Advance(start.Add(time.Minute), []Reading{reading("b", 3, 0, 0, 0)})
	if got := graphql(usage); got.Requests != 3 {
		t.Errorf("a new instance after a fresh scrape counts all it has: %+v", got)
	}
}

func TestTracker_ACounterThatWentBackIsReadAsAReset(t *testing.T) {
	tracker := NewTracker()
	tracker.Advance(start, []Reading{reading("a", 500, 0, 0, 0)})
	usage := tracker.Advance(start.Add(time.Minute), []Reading{reading("a", 4, 0, 0, 0)})
	if got := graphql(usage); got.Requests != 4 {
		t.Errorf("reset: %+v", got)
	}
}

func TestTracker_APodMissingForOneScrapeIsNotCountedTwice(t *testing.T) {
	tracker := NewTracker()
	tracker.Advance(start, []Reading{reading("a", 10, 0, 0, 0)})
	tracker.Advance(start.Add(time.Minute), nil)
	usage := tracker.Advance(start.Add(2*time.Minute), []Reading{reading("a", 12, 0, 0, 0)})
	if got := graphql(usage); got.Requests != 2 {
		t.Errorf("a pod that reappears continues from its last reading: %+v", got)
	}
}

func TestTracker_AfterAGapEverythingIsABaselineAgain(t *testing.T) {
	tracker := NewTracker()
	tracker.Advance(start, []Reading{reading("a", 10, 0, 0, 0)})
	// Another replica led meanwhile and recorded these requests.
	usage := tracker.Advance(start.Add(time.Hour), []Reading{reading("a", 900, 0, 0, 0), reading("b", 50, 0, 0, 0)})
	if got := graphql(usage); got.Requests != 0 {
		t.Errorf("a stale tracker must not claim another leader's counts: %+v", got)
	}
	usage = tracker.Advance(start.Add(time.Hour+time.Minute), []Reading{reading("a", 901, 0, 0, 0), reading("b", 52, 0, 0, 0)})
	if got := graphql(usage); got.Requests != 3 {
		t.Errorf("counting resumes after the baseline: %+v", got)
	}
}

func TestTracker_ErrorsAreSplitByOutcome(t *testing.T) {
	tracker := NewTracker()
	empty := Reading{Instance: "a", Histograms: map[SeriesKey][]float64{}}
	tracker.Advance(start, []Reading{empty})
	errorsOnly := Reading{Instance: "a", Histograms: map[SeriesKey][]float64{
		{Project: "p1", Family: "rest", Outcome: "client_error"}: filled(2),
		{Project: "p1", Family: "rest", Outcome: "server_error"}: filled(1),
		{Project: "p1", Family: "rest", Outcome: "ok"}:           filled(7),
	}}
	got := tracker.Advance(start.Add(time.Minute), []Reading{errorsOnly})[Key{Project: "p1", Family: "rest"}]
	if got.Requests != 10 || got.ClientErrors != 2 || got.ServerErrors != 1 {
		t.Errorf("rest: %+v", got)
	}
}

func filled(count float64) []float64 {
	buckets := make([]float64, BucketCount)
	for i := range buckets {
		buckets[i] = count
	}
	return buckets
}
