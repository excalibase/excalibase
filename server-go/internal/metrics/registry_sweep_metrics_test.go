package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRegistrySweepsAreCountedByResult(t *testing.T) {
	failedBefore := testutil.ToFloat64(registrySweeps.WithLabelValues("failed"))
	sweptBefore := testutil.ToFloat64(registrySweeps.WithLabelValues("swept"))
	CountRegistrySweep(false)
	CountRegistrySweep(true)
	CountRegistrySweep(false)
	if got := testutil.ToFloat64(registrySweeps.WithLabelValues("failed")) - failedBefore; got != 2 {
		t.Errorf("failed sweeps += %v, want 2", got)
	}
	if got := testutil.ToFloat64(registrySweeps.WithLabelValues("swept")) - sweptBefore; got != 1 {
		t.Errorf("sweeps += %v, want 1", got)
	}
}
