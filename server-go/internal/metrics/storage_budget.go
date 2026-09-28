package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// storageBytes is the platform's storage budget: the storage's capacity, the
// budget (a share of it) and what every volume reserves. The alerts at 70% and
// 80% of the budget are allocated / budget.
var storageBytes = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "excalibase_storage_bytes",
		Help: "The platform's storage: capacity, budget (the share volumes may take) and allocated (what every volume reserves).",
	},
	[]string{"kind"},
)

// SetStorageBudget exports the budget as last read.
func SetStorageBudget(capacity, budget, allocated int64) {
	storageBytes.WithLabelValues("capacity").Set(float64(capacity))
	storageBytes.WithLabelValues("budget").Set(float64(budget))
	storageBytes.WithLabelValues("allocated").Set(float64(allocated))
}
