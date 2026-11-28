package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestCreditMeterEventsAreCountedByReason(t *testing.T) {
	before := testutil.ToFloat64(creditMeterEvents.WithLabelValues(CreditMeterUnmeteredPlan))
	CountCreditMeterEvent(CreditMeterUnmeteredPlan)
	CountCreditMeterEvent(CreditMeterUnmeteredPlan)
	if got := testutil.ToFloat64(creditMeterEvents.WithLabelValues(CreditMeterUnmeteredPlan)) - before; got != 2 {
		t.Fatalf("unmetered plans += %v, want 2", got)
	}
	other := testutil.ToFloat64(creditMeterEvents.WithLabelValues(CreditMeterUnrecordedSize))
	if other != 0 {
		t.Fatalf("another reason must not move: %v", other)
	}
}
