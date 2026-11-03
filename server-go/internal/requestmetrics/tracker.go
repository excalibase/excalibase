package requestmetrics

import "time"

// staleAfter is how long a reading stays a usable baseline. Past it another
// replica may have led and recorded the counts in between, so all is baseline.
const staleAfter = 3 * time.Minute

type previous struct {
	reading Reading
	at      time.Time
}

// Tracker turns cumulative readings into what happened since the last scrape.
// Readings are keyed by the process's instance id, so a restarted pod (a new
// id, counters from zero) is never subtracted from its predecessor.
type Tracker struct {
	instances map[string]previous
	lastAt    time.Time
}

func NewTracker() *Tracker {
	return &Tracker{instances: map[string]previous{}}
}

// Advance records readings taken at now and answers the usage since the last
// call. An instance first seen right after a fresh scrape started since then,
// so all it counted is new; on the first call or after a gap it is a baseline.
func (t *Tracker) Advance(now time.Time, readings []Reading) map[Key]Usage {
	fresh := !t.lastAt.IsZero() && now.Sub(t.lastAt) <= staleAfter
	if !fresh {
		t.instances = map[string]previous{}
	}
	usage := map[Key]Usage{}
	for _, current := range readings {
		before, known := t.instances[current.Instance]
		if known || fresh {
			addHistograms(usage, current.Histograms, before.reading.Histograms)
			addRealtimeCounters(usage, current.Realtime, before.reading.Realtime)
		}
		addConnections(usage, current.Realtime)
		t.instances[current.Instance] = previous{reading: current, at: now}
	}
	for instance, kept := range t.instances {
		if now.Sub(kept.at) > staleAfter {
			delete(t.instances, instance)
		}
	}
	t.lastAt = now
	return usage
}

func addHistograms(usage map[Key]Usage, current, before map[SeriesKey][]float64) {
	for series, buckets := range current {
		cumulative := deltas(buckets, before[series])
		perBucket := make([]int64, BucketCount)
		var below int64
		for i, count := range cumulative {
			perBucket[i] = max(count-below, 0)
			below = max(count, below)
		}
		requests := cumulative[BucketCount-1]
		if requests == 0 {
			continue
		}
		span := Usage{Requests: requests, Buckets: perBucket}
		switch series.Outcome {
		case "client_error":
			span.ClientErrors = requests
		case "server_error":
			span.ServerErrors = requests
		}
		key := Key{Project: series.Project, Family: series.Family}
		usage[key] = usage[key].Merge(span)
	}
}

// deltas is current minus before per bucket; a series whose count went back
// restarted, so all it has now is new.
func deltas(current, before []float64) []int64 {
	out := make([]int64, BucketCount)
	reset := len(before) == BucketCount && current[BucketCount-1] < before[BucketCount-1]
	for i := range out {
		previousCount := 0.0
		if len(before) == BucketCount && !reset {
			previousCount = before[i]
		}
		out[i] = int64(max(current[i]-previousCount, 0))
	}
	return out
}

func addRealtimeCounters(usage map[Key]Usage, current, before map[string]Realtime) {
	for project, now := range current {
		then := before[project]
		span := Usage{Opened: counterDelta(now.Opened, then.Opened), Messages: counterDelta(now.Messages, then.Messages)}
		key := Key{Project: project, Family: FamilyRealtime}
		usage[key] = usage[key].Merge(span)
	}
}

func addConnections(usage map[Key]Usage, current map[string]Realtime) {
	for project, now := range current {
		key := Key{Project: project, Family: FamilyRealtime}
		span := usage[key]
		span.Connections += int64(now.Connections)
		usage[key] = span
	}
}

func counterDelta(now, then float64) int64 {
	if now < then {
		return int64(now)
	}
	return int64(now - then)
}
