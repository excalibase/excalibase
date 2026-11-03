// Package requestmetrics turns the request counters graphql and auth expose on
// their internal port into per-project usage for Studio's request charts
// (EXC-656): requests, errors, latency percentiles and realtime activity.
package requestmetrics

import "math"

// Bounds are the latency bucket upper bounds in seconds, the same in the
// engine and in auth so histograms merge across pods and services.
var Bounds = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// BucketCount is every bound plus the bucket past the last one.
var BucketCount = len(Bounds) + 1

// FamilyRealtime is the WebSocket activity of a project; the others are request families.
const FamilyRealtime = "realtime"

var requestFamilies = map[string]bool{"graphql": true, "rest": true, "auth": true}

// Families are what Studio charts, in order.
var Families = []string{"graphql", "rest", "auth", FamilyRealtime}

// Key is one project's family.
type Key struct {
	Project string
	Family  string
}

// Usage is what a project's family did over a span of time. Buckets are
// per-bucket counts, not cumulative. Connections is a reading: open now.
type Usage struct {
	Requests     int64
	ClientErrors int64
	ServerErrors int64
	Buckets      []int64
	Connections  int64
	Opened       int64
	Messages     int64
}

// Merge adds two spans of the same family; open connections keep the peak.
func (u Usage) Merge(other Usage) Usage {
	return Usage{
		Requests:     u.Requests + other.Requests,
		ClientErrors: u.ClientErrors + other.ClientErrors,
		ServerErrors: u.ServerErrors + other.ServerErrors,
		Buckets:      addBuckets(u.Buckets, other.Buckets),
		Connections:  max(u.Connections, other.Connections),
		Opened:       u.Opened + other.Opened,
		Messages:     u.Messages + other.Messages,
	}
}

// Empty is a span with nothing to record.
func (u Usage) Empty() bool {
	return u.Requests == 0 && u.Connections == 0 && u.Opened == 0 && u.Messages == 0
}

func addBuckets(a, b []int64) []int64 {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	sum := make([]int64, BucketCount)
	for i := range sum {
		if i < len(a) {
			sum[i] += a[i]
		}
		if i < len(b) {
			sum[i] += b[i]
		}
	}
	return sum
}

// QuantileMs estimates the q-quantile in milliseconds by linear interpolation
// inside its bucket, as Prometheus' histogram_quantile does; past the last
// bound it answers the last bound. False when there were no requests.
func QuantileMs(buckets []int64, q float64) (float64, bool) {
	var total int64
	for _, count := range buckets {
		total += count
	}
	if total == 0 {
		return 0, false
	}
	rank := q * float64(total)
	var below int64
	for i, count := range buckets {
		if count > 0 && float64(below+count) >= rank {
			if i >= len(Bounds) {
				return Bounds[len(Bounds)-1] * 1000, true
			}
			lower := 0.0
			if i > 0 {
				lower = Bounds[i-1]
			}
			fraction := (rank - float64(below)) / float64(count)
			return (lower + (Bounds[i]-lower)*math.Max(fraction, 0)) * 1000, true
		}
		below += count
	}
	return Bounds[len(Bounds)-1] * 1000, true
}
