package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Reasons the credit meter did something an operator should be able to see (EXC-866).
const (
	// CreditMeterUnmeteredPlan: an org on a plan the meter does not charge was not sampled.
	CreditMeterUnmeteredPlan = "unmetered_plan"
	// CreditMeterUnrecordedSize: a database whose size was never recorded was billed at its plan's default size.
	CreditMeterUnrecordedSize = "unrecorded_size"
	// CreditMeterRepricedHour: an hour already on the ledger came out at a different amount on a retry and was kept as billed.
	CreditMeterRepricedHour = "repriced_hour"
	// CreditMeterShortfallHour: the credit could not pay all of an hour.
	CreditMeterShortfallHour = "shortfall_hour"
	// CreditMeterDroppedPostStepSample: a sample after a step gave up waiting for the step to commit; the next regular sample covers it.
	CreditMeterDroppedPostStepSample = "dropped_post_step_sample"
)

var creditMeterEvents = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "excalibase_credit_meter_events_total",
		Help: "Credit meter cases an operator should see, by reason.",
	},
	[]string{"reason"},
)

// CountCreditMeterEvent records one credit meter case.
func CountCreditMeterEvent(reason string) {
	creditMeterEvents.WithLabelValues(reason).Inc()
}
