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

// suspensionMails counts the mails an account suspension sends to the account
// holder, so a mail that keeps failing is seen (EXC-841).
var suspensionMails = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "excalibase_suspension_mail_total",
		Help: "Suspension mails to account holders, by outcome: sent or failed.",
	},
	[]string{"outcome"},
)

// SuspensionMail records one suspension mail's outcome.
func SuspensionMail(outcome string) { suspensionMails.WithLabelValues(outcome).Inc() }
