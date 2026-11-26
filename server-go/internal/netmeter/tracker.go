package netmeter

import "time"

// minSpan is the shortest span a rate is divided over; closer readings give no rate.
const minSpan = time.Second

// Tracker turns cumulative counters into rates. It is not safe for concurrent use.
type Tracker struct {
	previous map[string]Reading
	primed   bool
}

func NewTracker() *Tracker { return &Tracker{previous: map[string]Reading{}} }

// Reset forgets every baseline, as when this replica has just become the leader:
// the next Observe records baselines only.
func (t *Tracker) Reset() {
	t.previous = map[string]Reading{}
	t.primed = false
}

// Observe returns the rate of each instance seen before and keeps these
// readings as the next baselines. Rules:
//   - the first Observe after a start or Reset only records baselines;
//   - a counter that went down was reset, and the new value is the delta;
//   - an instance first seen later is judged by its start time, and with no
//     start time only gets a baseline;
//   - a reading no later than a second after its baseline gives no rate and
//     keeps the older baseline;
//   - an instance missing from a round loses its baseline; what it sent
//     between its last reading and its end is not counted;
//   - an instance named in unobserved was not read because its source failed,
//     not because it ended: it keeps its baseline for the round that follows.
func (t *Tracker) Observe(readings []Reading, unobserved ...string) []Rate {
	var rates []Rate
	next := make(map[string]Reading, len(readings))
	if t.primed {
		for _, key := range unobserved {
			if baseline, seen := t.previous[key]; seen {
				next[key] = baseline
			}
		}
	}
	for _, current := range readings {
		rate, ok, baseline := t.judge(current)
		if ok {
			rates = append(rates, rate)
		}
		next[current.Key] = baseline
	}
	t.previous, t.primed = next, true
	return rates
}

// judge returns the rate of one reading, if any, and the baseline to keep for it.
func (t *Tracker) judge(current Reading) (Rate, bool, Reading) {
	if !t.primed {
		return Rate{}, false, current
	}
	baseline, seen := t.previous[current.Key]
	if !seen {
		rate, ok := sinceStart(current)
		return rate, ok, current
	}
	rate, ok := rateBetween(baseline, current)
	if !ok {
		return Rate{}, false, baseline
	}
	return rate, true, current
}

func rateBetween(from, to Reading) (Rate, bool) {
	span := to.At.Sub(from.At)
	if span < minSpan {
		return Rate{}, false
	}
	seconds := span.Seconds()
	return Rate{
		Key: to.Key, ProjectID: to.ProjectID, AppID: to.AppID,
		RxBps: float64(delta(from.Rx, to.Rx)) / seconds,
		TxBps: float64(delta(from.Tx, to.Tx)) / seconds,
	}, true
}

// sinceStart judges an instance first seen now by what it moved since it started.
func sinceStart(current Reading) (Rate, bool) {
	if current.Started.IsZero() {
		return Rate{}, false
	}
	return rateBetween(Reading{Key: current.Key, At: current.Started}, current)
}

// delta is the bytes added between two cumulative readings; a lower value is a counter reset.
func delta(before, after uint64) uint64 {
	if after < before {
		return after
	}
	return after - before
}
