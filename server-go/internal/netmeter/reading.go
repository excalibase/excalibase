package netmeter

import (
	"sort"
	"time"
)

// Reading is one instance's (pod or container) cumulative byte counters at a moment.
type Reading struct {
	// Key is stable for the instance's life: a pod UID or a container ID.
	Key       string
	ProjectID string
	AppID     string
	// At is when the counters were read, by the source's own clock.
	At time.Time
	// Started is when the instance started; zero when the source does not say.
	Started time.Time
	Rx, Tx  uint64
}

// Rate is one instance's bytes per second over the span between two readings.
type Rate struct {
	Key       string
	ProjectID string
	AppID     string
	RxBps     float64
	TxBps     float64
}

// Sample is one app's minute: its instances' rates added up.
type Sample struct {
	At        time.Time
	ProjectID string
	AppID     string
	Instances int
	RxBps     float64
	TxBps     float64
	// MaxInstanceTxBps is the busiest single instance, which a per-pod cap is judged against.
	MaxInstanceTxBps float64
	// OwnerID and Tier are who owned the project and its tier when the minute was taken, so the history
	// outlives the project's deletion. Empty when the meter could not say.
	OwnerID string
	Tier    string
}

// Aggregate adds the instances of each app together, ordered by project then app.
// A rate that names no project or app is dropped: it cannot be attributed.
func Aggregate(at time.Time, rates []Rate) []Sample {
	type key struct{ project, app string }
	byApp := map[key]*Sample{}
	for _, rate := range rates {
		if rate.ProjectID == "" || rate.AppID == "" {
			continue
		}
		id := key{rate.ProjectID, rate.AppID}
		sample := byApp[id]
		if sample == nil {
			sample = &Sample{At: at, ProjectID: rate.ProjectID, AppID: rate.AppID}
			byApp[id] = sample
		}
		sample.Instances++
		sample.RxBps += rate.RxBps
		sample.TxBps += rate.TxBps
		sample.MaxInstanceTxBps = max(sample.MaxInstanceTxBps, rate.TxBps)
	}
	out := make([]Sample, 0, len(byApp))
	for _, sample := range byApp {
		out = append(out, *sample)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProjectID != out[j].ProjectID {
			return out[i].ProjectID < out[j].ProjectID
		}
		return out[i].AppID < out[j].AppID
	})
	return out
}

// Hourly is one app's finished hour, rolled up from its minutes.
type Hourly struct {
	Hour             time.Time
	ProjectID        string
	AppID            string
	Samples          int
	AvgRxBps         float64
	AvgTxBps         float64
	MaxTxBps         float64
	MaxInstanceTxBps float64
	// OwnerID and Tier are the snapshot carried by the hour's minutes.
	OwnerID string
	Tier    string
}
