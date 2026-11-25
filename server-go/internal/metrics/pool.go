package metrics

import (
	"database/sql"
	"errors"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var poolRejected = promauto.NewCounter(prometheus.CounterOpts{
	Name: "excalibase_db_pool_rejected_total",
	Help: "Requests answered 503 because every platform-database connection stayed in use.",
})

// CountPoolRejected records one request refused for an exhausted pool.
func CountPoolRejected() { poolRejected.Inc() }

// RegisterPoolStats exposes the pool's own counters on the default registry:
// go_sql_max_open_connections, go_sql_open_connections, go_sql_in_use_connections,
// go_sql_idle_connections, go_sql_wait_count_total and
// go_sql_wait_duration_seconds_total, labelled db_name. Registering the same
// name twice is a no-op.
func RegisterPoolStats(db *sql.DB, name string) error {
	err := prometheus.Register(collectors.NewDBStatsCollector(db, name))
	var already prometheus.AlreadyRegisteredError
	if err != nil && !errors.As(err, &already) {
		return err
	}
	return nil
}
