package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// crashLoopTrips counts how often the crash-loop guard found a workload over
// its threshold: an app it stopped, or a database it only alerted on (EXC-839).
var crashLoopTrips = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "excalibase_crash_loop_trips_total",
		Help: "Workloads found crash-looping over the threshold, by kind: app (stopped) or database (alert only).",
	},
	[]string{"kind"},
)

// CrashLoopTripped records one workload over the threshold.
func CrashLoopTripped(kind string) { crashLoopTrips.WithLabelValues(kind).Inc() }
