package scheduler

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/robfig/cron/v3"
)

// CronRunnerConfig wires the cron runner's collaborators.
type CronRunnerConfig struct {
	DB *sql.DB
	// Logger is optional; defaults to the std log package.
	Logger *log.Logger
	// IDGen overrides the scheduled-task id generator (tests use a
	// deterministic stub; production uses base32Rand).
	IDGen func() string
}

// CronRunner walks the cron registry and enqueues the next due
// excalibase_scheduled_functions row per job. One CronRunner per replica;
// the per-row update to `last_enqueued_at` is the idempotency lock that
// keeps concurrent runners from double-enqueuing.
type CronRunner struct {
	db     *sql.DB
	logger *log.Logger
	idGen  func() string
	parser cron.Parser
}

func NewCronRunner(c CronRunnerConfig) *CronRunner {
	logger := c.Logger
	if logger == nil {
		logger = log.Default()
	}
	idGen := c.IDGen
	if idGen == nil {
		idGen = base32RandID
	}
	return &CronRunner{
		db:     c.DB,
		logger: logger,
		idGen:  idGen,
		// Standard 5-field cron expression — matches what cronJobs.cron()
		// validates on the lib side. Robfig/cron's default parser
		// expects the optional seconds field; we strip the seconds slot
		// by using the standard parser explicitly.
		parser: cron.NewParser(
			cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow,
		),
	}
}

// Run polls every minute (matches the typical cron granularity) until the
// context is cancelled. The per-iteration cadence is intentionally fixed
// at 60s because that's the smallest granularity the standard 5-field
// cron expression can express anyway.
func (cr *CronRunner) Run(ctx context.Context) error {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := cr.Tick(ctx); err != nil {
				cr.logger.Printf("scheduler.CronRunner tick: %v", err)
			}
		}
	}
}

// Tick walks the registry and enqueues each job whose next due time has
// arrived since `last_enqueued_at`. Exported so tests can drive a
// deterministic cycle without waiting for the 60s ticker.
func (cr *CronRunner) Tick(ctx context.Context) error {
	rows, err := cr.db.QueryContext(ctx, `
		SELECT name, project_id, module_name, export_name, args, schedule, last_enqueued_at
		  FROM excalibase_cron_jobs
		 ORDER BY project_id, name
	`)
	if err != nil {
		return fmt.Errorf("list cron jobs: %w", err)
	}
	defer rows.Close()
	now := time.Now()
	for rows.Next() {
		var (
			name, projectID, moduleName, exportName string
			args, scheduleRaw                       []byte
			lastEnqueued                            sql.NullTime
		)
		if err := rows.Scan(&name, &projectID, &moduleName, &exportName, &args, &scheduleRaw, &lastEnqueued); err != nil {
			return fmt.Errorf("scan cron row: %w", err)
		}
		var schedule struct {
			Kind        string `json:"kind"`
			Expression  string `json:"expression,omitempty"`
			Hours       int    `json:"hours,omitempty"`
			Minutes     int    `json:"minutes,omitempty"`
			Seconds     int    `json:"seconds,omitempty"`
			HourUTC     int    `json:"hourUTC,omitempty"`
			MinuteUTC   int    `json:"minuteUTC,omitempty"`
		}
		if err := json.Unmarshal(scheduleRaw, &schedule); err != nil {
			cr.logger.Printf("scheduler: cron %s/%s bad schedule: %v", projectID, name, err)
			continue
		}
		nextDue, ok := nextDueAt(schedule, now, cr.parser)
		if !ok {
			cr.logger.Printf("scheduler: cron %s/%s unsupported schedule kind=%q", projectID, name, schedule.Kind)
			continue
		}
		// Skip when we've already enqueued the upcoming due time. The
		// runner tick interval is 60s, so this guards against the
		// 1-minute window where the same nextDue would otherwise be
		// inserted twice.
		if lastEnqueued.Valid && !nextDue.After(lastEnqueued.Time) {
			continue
		}
		// Insert the row and bump last_enqueued_at in a single transaction
		// so the enqueue + bookkeeping advance together.
		if err := cr.enqueue(ctx, projectID, name, moduleName, exportName, args, nextDue); err != nil {
			cr.logger.Printf("scheduler: cron %s/%s enqueue: %v", projectID, name, err)
			continue
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

func (cr *CronRunner) enqueue(
	ctx context.Context,
	projectID, name, moduleName, exportName string,
	args json.RawMessage,
	nextDue time.Time,
) error {
	tx, err := cr.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	id := cr.idGen()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO excalibase_scheduled_functions
		  (id, project_id, module_name, export_name, args, scheduled_for, status)
		VALUES ($1, $2, $3, $4, $5, $6, 'pending')
	`, id, projectID, moduleName, exportName, args, nextDue); err != nil {
		return fmt.Errorf("insert task: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE excalibase_cron_jobs
		   SET last_enqueued_at = $3
		 WHERE project_id = $1 AND name = $2
	`, projectID, name, nextDue); err != nil {
		return fmt.Errorf("bump last_enqueued_at: %w", err)
	}
	return tx.Commit()
}

// nextDueAt translates a registry schedule into the absolute timestamp
// at which the next enqueue must fire. Returns (time, true) on a known
// schedule kind; (zero, false) otherwise.
//
// The 4 supported kinds mirror the lib's `cronJobs()` registry:
//
//   * cron       — 5-field expression, parsed by robfig/cron;
//   * interval   — fixed period in (hours, minutes, seconds);
//   * daily      — fires once a day at (hourUTC, minuteUTC);
//   * hourly     — fires every hour at (minuteUTC).
//
// All times are UTC.
func nextDueAt(s struct {
	Kind       string `json:"kind"`
	Expression string `json:"expression,omitempty"`
	Hours      int    `json:"hours,omitempty"`
	Minutes    int    `json:"minutes,omitempty"`
	Seconds    int    `json:"seconds,omitempty"`
	HourUTC    int    `json:"hourUTC,omitempty"`
	MinuteUTC  int    `json:"minuteUTC,omitempty"`
}, now time.Time, parser cron.Parser) (time.Time, bool) {
	switch s.Kind {
	case "cron":
		sched, err := parser.Parse(s.Expression)
		if err != nil {
			return time.Time{}, false
		}
		return sched.Next(now), true
	case "interval":
		d := time.Duration(s.Hours)*time.Hour +
			time.Duration(s.Minutes)*time.Minute +
			time.Duration(s.Seconds)*time.Second
		if d <= 0 {
			return time.Time{}, false
		}
		return now.Add(d), true
	case "daily":
		next := nextDailyUTC(now, s.HourUTC, s.MinuteUTC)
		return next, true
	case "hourly":
		next := nextHourlyUTC(now, s.MinuteUTC)
		return next, true
	default:
		return time.Time{}, false
	}
}

// nextDailyUTC returns the next time today (or tomorrow if already past)
// at which (hourUTC, minuteUTC) occurs. UTC always.
func nextDailyUTC(now time.Time, hourUTC, minuteUTC int) time.Time {
	utc := now.UTC()
	candidate := time.Date(utc.Year(), utc.Month(), utc.Day(), hourUTC, minuteUTC, 0, 0, time.UTC)
	if !candidate.After(utc) {
		candidate = candidate.Add(24 * time.Hour)
	}
	return candidate
}

// nextHourlyUTC returns the next time within the next hour at which
// minute=minuteUTC occurs.
func nextHourlyUTC(now time.Time, minuteUTC int) time.Time {
	utc := now.UTC()
	candidate := time.Date(utc.Year(), utc.Month(), utc.Day(), utc.Hour(), minuteUTC, 0, 0, time.UTC)
	if !candidate.After(utc) {
		candidate = candidate.Add(1 * time.Hour)
	}
	return candidate
}

// base32RandID produces a 30-character base32 token (matches the runtime's
// runtime/ids.ts shape exactly).
const base32Alphabet = "abcdefghijklmnopqrstuvwxyz234567"

func base32RandID() string {
	var buf [30]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand failing is fatal in practice; fall back to a
		// time-derived token so we don't crash the runner.
		ns := time.Now().UnixNano()
		for i := range buf {
			buf[i] = base32Alphabet[ns&31]
			ns >>= 5
			if ns == 0 {
				ns = time.Now().UnixNano()
			}
		}
		return string(buf[:])
	}
	for i, b := range buf {
		buf[i] = base32Alphabet[int(b)&31]
	}
	return string(buf[:])
}
