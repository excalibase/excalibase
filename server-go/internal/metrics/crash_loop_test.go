package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestCrashLoopTrippedCountsByKind(t *testing.T) {
	before := testutil.ToFloat64(crashLoopTrips.WithLabelValues("database"))
	CrashLoopTripped("database")
	CrashLoopTripped("database")
	if got := testutil.ToFloat64(crashLoopTrips.WithLabelValues("database")) - before; got != 2 {
		t.Errorf("database trips rose by %v, want 2", got)
	}
}
