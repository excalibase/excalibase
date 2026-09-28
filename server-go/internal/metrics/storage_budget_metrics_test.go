package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestStorageBudgetIsExported(t *testing.T) {
	SetStorageBudget(100, 80, 56)
	for name, want := range map[string]float64{"capacity": 100, "budget": 80, "allocated": 56} {
		if got := testutil.ToFloat64(storageBytes.WithLabelValues(name)); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
}
