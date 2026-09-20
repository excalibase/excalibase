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
	// ProjectID is the project whose database the sweep opened. The registry
	// is tenant-written, so this — not a row's project_id — is the identity
	// every enqueued task carries.
	ProjectID string
	// MinInterval is the finest cadence the platform honours. A schedule
	// asking for less is clipped up to it. Default 1 minute.
	MinInterval time.Duration
	// MaxJobs caps how many registry rows one tick reads. Default 100.
	MaxJobs int
	// MaxArgsBytes caps a registry row's args and schedule. Default matches
	// the public invoke body limit.
	MaxArgsBytes int
	// Functions is the platform's registry of deployed functions. A registry
	// row may only enqueue a module the platform itself deployed for this
	// project; without it nothing is enqueued.
	Functions FunctionRegistry
	// Logger is optional; defaults to the std log package.
	Logger *log.Logger
	// IDGen overrides the scheduled-task id generator (tests use a
	// deterministic stub; production uses base32Rand).
	IDGen func() (string, error)
}

// DefaultCronMinInterval is the finest cron cadence the platform runs.
const DefaultCronMinInterval = time.Minute

// DefaultCronMaxJobs caps the registry rows read per project per tick.
const DefaultCronMaxJobs = 100

// CronRunner walks the cron registry and enqueues the next due
// excalibase.excalibase_scheduled_functions row per job. One CronRunner per replica;
// the per-row update to `last_enqueued_at` is the idempotency lock that
// keeps concurrent runners from double-enqueuing.
type CronRunner struct {
	db           *sql.DB
	projectID    string
	minInterval  time.Duration
	maxJobs      int
	maxArgsBytes int
	functions    FunctionRegistry
	logger       *log.Logger
	idGen        func() (string, error)
	parser       cron.Parser
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
	minInterval := c.MinInterval
	if minInterval <= 0 {
		minInterval = DefaultCronMinInterval
	}
	maxJobs := c.MaxJobs
	if maxJobs <= 0 {
		maxJobs = DefaultCronMaxJobs
	}
	maxArgs := c.MaxArgsBytes
	if maxArgs <= 0 {
		maxArgs = DefaultMaxArgsBytes
	}
	return &CronRunner{
		db:           c.DB,
		projectID:    c.ProjectID,
		minInterval:  minInterval,
		maxJobs:      maxJobs,
		maxArgsBytes: maxArgs,
		functions:    c.Functions,
		logger:       logger,
		idGen:        idGen,
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

// cronListSQL reads one project's registry. Every column it returns is
// tenant-written, so the bounds are in the WHERE clause: a row that would
// not pass the Go-side checks anyway is never read into memory, and neither
// is an enormous args or schedule payload. The registry has no status
// column, so an out-of-bounds row is simply not walked — the next deploy
// rewrites it.
const cronListSQL = `
		SELECT name, project_id, module_name, export_name, args, schedule, last_enqueued_at
		  FROM excalibase.excalibase_cron_jobs
		 WHERE project_id = $1
		   AND octet_length(args::text) <= $3
		   AND octet_length(schedule::text) <= $3
		   AND length(name) <= $4
		   AND length(module_name) <= $5
		   AND length(export_name) <= $6
		 ORDER BY name
		 LIMIT $2
	`

// Tick walks the registry and enqueues each job whose next due time has
// arrived since `last_enqueued_at`. Exported so tests can drive a
// deterministic cycle without waiting for the 60s ticker.
func (cr *CronRunner) Tick(ctx context.Context) error {
	rows, err := cr.db.QueryContext(ctx, cronListSQL,
		cr.projectID, cr.maxJobs,
		cr.maxArgsBytes, maxCronNameLen, maxModuleNameLen, maxExportNameLen)
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
		// Tenant-written columns: the row may only speak for the project
		// whose database this is, and only through plain identifiers.
		if projectID != cr.projectID {
			cr.logger.Printf("scheduler: cron %s refused: names another project", name)
			continue
		}
		if !validModuleName(moduleName) || !validExportName(exportName) {
			cr.logger.Printf("scheduler: cron %s refused: module or export is not an identifier", name)
			continue
		}
		// Same rule as the task half: a registry row may only name a module
		// the platform deployed for this project. Enqueueing anything else
		// would fill the queue with rows the worker then has to refuse, at
		// the tenant's chosen cadence.
		if !cr.deployed(name, moduleName) {
			continue
		}
		var schedule struct {
			Kind       string `json:"kind"`
			Expression string `json:"expression,omitempty"`
			Hours      int    `json:"hours,omitempty"`
			Minutes    int    `json:"minutes,omitempty"`
			Seconds    int    `json:"seconds,omitempty"`
			HourUTC    int    `json:"hourUTC,omitempty"`
			MinuteUTC  int    `json:"minuteUTC,omitempty"`
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
		// A schedule finer than the platform minimum is clipped: the cadence
		// a tenant asks for cannot set the platform's load.
		if earliest := now.Add(cr.minInterval); nextDue.Before(earliest) {
			nextDue = earliest
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
		if err := cr.enqueue(ctx, cr.projectID, name, moduleName, exportName, args, nextDue); err != nil {
			cr.logger.Printf("scheduler: cron %s/%s enqueue: %v", projectID, name, err)
			continue
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

// deployed asks the platform registry whether this project has the module.
// A registry that is absent or cannot answer means no, never a guess.
func (cr *CronRunner) deployed(name, moduleName string) bool {
	if cr.functions == nil {
		cr.logger.Printf("scheduler: cron %s refused: no function registry wired", name)
		return false
	}
	known, err := cr.functions.HasFunction(cr.projectID, moduleName)
	if err != nil || !known {
		cr.logger.Printf("scheduler: cron %s refused: no such function is deployed", name)
		return false
	}
	return true
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
	id, err := cr.idGen()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO excalibase.excalibase_scheduled_functions
		  (id, project_id, module_name, export_name, args, scheduled_for, status)
		VALUES ($1, $2, $3, $4, $5, $6, 'pending')
	`, id, projectID, moduleName, exportName, args, nextDue); err != nil {
		return fmt.Errorf("insert task: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE excalibase.excalibase_cron_jobs
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
//   - cron       — 5-field expression, parsed by robfig/cron;
//   - interval   — fixed period in (hours, minutes, seconds);
//   - daily      — fires once a day at (hourUTC, minuteUTC);
//   - hourly     — fires every hour at (minuteUTC).
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

// randRead is the entropy source, replaceable in tests.
var randRead = rand.Read

func base32RandID() (string, error) {
	var buf [30]byte
	if _, err := randRead(buf[:]); err != nil {
		// A guessable task id is worse than no task: refuse rather than
		// fall back to anything derived from the clock.
		return "", fmt.Errorf("generate task id: %w", err)
	}
	for i, b := range buf {
		buf[i] = base32Alphabet[int(b)&31]
	}
	return string(buf[:]), nil
}
