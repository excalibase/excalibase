package service

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

const (
	poolMetric = "platform.db_pool"
	// poolFullFor is how long every connection must stay in use before the
	// pool counts as exhausted: bursts fill it for seconds without harm.
	poolFullFor = 5 * time.Minute
	// poolSampleEvery is how often the pool is read.
	poolSampleEvery = 30 * time.Second
)

// PoolPressureMonitor raises a platform alert when the platform database pool
// stays at its ceiling (EXC-746). The platform has no Prometheus, so the rule
// lives here and goes through the same alerting path as the pod-pressure check.
type PoolPressureMonitor struct {
	stats    func() sql.DBStats
	alerts   *AlertingService
	now      func() time.Time
	notifier poolAlertNotifier

	mu    sync.Mutex
	since time.Time // when the pool was last seen reaching the ceiling; zero when below it
	busy  atomic.Bool
}

// poolAlertNotifier is satisfied by the admin alert mail (AlertNotifier).
type poolAlertNotifier interface {
	Notify(ctx context.Context, change AlertChange)
}

// NewPoolPressureMonitor watches the pool whose counters stats returns.
func NewPoolPressureMonitor(stats func() sql.DBStats, alerts *AlertingService, now func() time.Time) *PoolPressureMonitor {
	return &PoolPressureMonitor{stats: stats, alerts: alerts, now: now}
}

// SetNotifier sends every raised or resolved alert on; without one it is only recorded.
func (m *PoolPressureMonitor) SetNotifier(notifier poolAlertNotifier) { m.notifier = notifier }

// Sample reads the pool once: it raises after poolFullFor at the ceiling and
// resolves as soon as a connection is free again.
func (m *PoolPressureMonitor) Sample(ctx context.Context) {
	stats := m.stats()
	now := m.now()
	atCeiling := stats.MaxOpenConnections > 0 && stats.InUse >= stats.MaxOpenConnections

	m.mu.Lock()
	switch {
	case !atCeiling:
		m.since = time.Time{}
	case m.since.IsZero():
		m.since = now
	}
	breached := atCeiling && now.Sub(m.since) >= poolFullFor
	m.mu.Unlock()

	change := m.alerts.Track("", poolMetric, breached, func() domain.Alert {
		percent := float64(stats.InUse) / float64(stats.MaxOpenConnections) * 100
		return domain.Alert{
			ID: fmt.Sprintf("platform.db_pool-%d", now.UnixNano()), Severity: "CRITICAL",
			Message: fmt.Sprintf("Provisioning has used all %d platform database connections for %d minutes (%d waiting requests so far); requests that need the database are refused with 503",
				stats.MaxOpenConnections, int(poolFullFor.Minutes()), stats.WaitCount),
			Value: &percent, Timestamp: &domain.FlexTime{Time: now},
		}
	})
	if m.notifier != nil && (change.Raised != nil || change.Resolved != nil) {
		m.notifier.Notify(ctx, change)
	}
}

// Run samples until ctx ends. Recording or mailing an alert needs a database
// connection, which is the thing that ran out, so a sample that is still
// waiting for one is never joined by another.
func (m *PoolPressureMonitor) Run(ctx context.Context) {
	ticker := time.NewTicker(poolSampleEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if m.busy.CompareAndSwap(false, true) {
				go func() {
					defer m.busy.Store(false)
					m.Sample(ctx)
				}()
			}
		}
	}
}
