package service

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// Audit actions the idle-pause sweep writes. Both carry Resource "project"
// and ResourceID = project id; UserID is empty (system actor).
const (
	AuditActionIdleWarning = "project.idle_warning"
	AuditActionIdlePause   = "project.idle_pause"
)

const (
	DefaultIdlePauseInterval = time.Hour
	// DefaultResumeGrace keeps a freshly resumed project out of the sweep so a
	// resume can never be undone by the very next tick.
	DefaultResumeGrace  = 24 * time.Hour
	idlePauseTickBudget = 30 * time.Minute
	idleDay             = 24 * time.Hour

	// A pause that fails leaves the project in PAUSING, and only another
	// pause can move it. The sweep retries those, but a pause that fails
	// every time — invalid backup credentials, say — would otherwise file a
	// Backup CR on every tick.
	//
	// The spacing doubles per failed attempt from pauseRetryBackoffBase up
	// to pauseRetryBackoffCeiling, and both the count and the last attempt
	// live on the project row. That is the point: a restart or a leader
	// change hands the next replica a backoff that has already grown,
	// rather than starting the storm over.
	pauseRetryBackoffBase    = 15 * time.Minute
	pauseRetryBackoffCeiling = 6 * time.Hour
)

// pauseRetryBackoffFor is how long to wait before the next attempt, given
// how many have already failed. Doubling, with a ceiling: at the ceiling a
// permanently stuck project costs four attempts a day, not one an hour.
func pauseRetryBackoffFor(attempts int) time.Duration {
	backoff := pauseRetryBackoffBase
	for i := 0; i < attempts; i++ {
		backoff *= 2
		if backoff >= pauseRetryBackoffCeiling {
			return pauseRetryBackoffCeiling
		}
	}
	return backoff
}

// pauseRetryDue reports whether the backoff recorded on the row has elapsed.
// A project that has never been attempted is due immediately.
func pauseRetryDue(inst *domain.DatabaseInstance, now time.Time) bool {
	if inst.PauseLastAttemptAt == nil {
		return true
	}
	return now.Sub(inst.PauseLastAttemptAt.Time) >= pauseRetryBackoffFor(inst.PauseAttempts)
}

// TierResolver returns the effective tier spec (store row or built-in
// fallback). ProvisioningService.TierConfig satisfies it.
type TierResolver func(ctx context.Context, tier domain.TierType) (config.TierConfig, error)

// IdlePauser is the slice of PauseService the sweep needs.
type IdlePauser interface {
	Pause(ctx context.Context, projectID, reason string) error
}

// IdleResumer lets the sweep finish a resume that stopped part way. A project
// left in RESUMING has its database up and its replication off: a degraded
// tenant that nothing else will ever notice, because only the pause path was
// being retried.
type IdleResumer interface {
	Resume(ctx context.Context, projectID string) error
}

// IdleWarnNotifier delivers the day-(N-1) warning to humans. Optional: the
// warning is always written to the audit log and the project's activity row
// regardless of delivery.
type IdleWarnNotifier interface {
	NotifyIdleWarning(ctx context.Context, inst *domain.DatabaseInstance, pauseAt time.Time) error
}

// AuditWriter is the single audit method the sweep uses.
type AuditWriter interface {
	LogAudit(ctx context.Context, entry *domain.AuditEntry) error
}

// IdlePauseSchedulerConfig wires the sweep. Instances, Activity, Tiers,
// Pauser and Lock are required; the rest are optional.
type IdlePauseSchedulerConfig struct {
	Instances storage.InstanceStore
	Activity  storage.ProjectActivityStore
	Tiers     TierResolver
	Pauser    IdlePauser
	Resumer   IdleResumer
	Notifier  IdleWarnNotifier
	Audit     AuditWriter
	Lock      storage.LeaderLock
	// Now is injectable for tests; defaults to time.Now.
	Now func() time.Time
	// Interval between sweeps; defaults to DefaultIdlePauseInterval.
	Interval time.Duration
	// ResumeGrace defaults to DefaultResumeGrace.
	ResumeGrace time.Duration
	Logger      *log.Logger
}

// IdlePauseReport lists what one sweep did, by project id.
type IdlePauseReport struct {
	Warned  []string
	Paused  []string
	Resumed []string
	Failed  []string
}

// IdlePauseScheduler pauses idle projects. Every Interval it takes the
// leader lock and, for each ACTIVE project on a tier with
// AutoPauseAfterDays = N > 0, compares the effective last-seen time
// (max of project_activity, created_at and last resume) against now:
// idle >= N days → pause; idle >= N-1 days → warn once per idle stretch.
type IdlePauseScheduler struct {
	instances   storage.InstanceStore
	activity    storage.ProjectActivityStore
	tiers       TierResolver
	pauser      IdlePauser
	resumer     IdleResumer
	notifier    IdleWarnNotifier
	audit       AuditWriter
	leadership  *Leadership
	now         func() time.Time
	interval    time.Duration
	resumeGrace time.Duration
	logger      *log.Logger

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	running bool
}

func NewIdlePauseScheduler(c IdlePauseSchedulerConfig) *IdlePauseScheduler {
	s := &IdlePauseScheduler{
		instances: c.Instances, activity: c.Activity, tiers: c.Tiers, pauser: c.Pauser, resumer: c.Resumer,
		notifier: c.Notifier, audit: c.Audit, leadership: NewLeadership(c.Lock),
		now: c.Now, interval: c.Interval, resumeGrace: c.ResumeGrace, logger: c.Logger,
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.interval <= 0 {
		s.interval = DefaultIdlePauseInterval
	}
	if s.resumeGrace <= 0 {
		s.resumeGrace = DefaultResumeGrace
	}
	if s.logger == nil {
		s.logger = log.Default()
	}
	return s
}

// Start launches the periodic sweep. Idempotent.
func (s *IdlePauseScheduler) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel, s.done, s.running = cancel, make(chan struct{}), true
	go s.loop(runCtx, s.done)
}

// Stop halts the sweep and waits for an in-flight tick to finish.
func (s *IdlePauseScheduler) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	cancel, done := s.cancel, s.done
	s.running = false
	s.mu.Unlock()
	cancel()
	<-done
	// Hand the claim back so another replica leads immediately.
	if err := s.leadership.Close(context.Background()); err != nil {
		s.logger.Printf("idle-pause: stand down: %v", err)
	}
}

func (s *IdlePauseScheduler) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

// tick runs one leader-guarded sweep with a bounded budget.
func (s *IdlePauseScheduler) tick(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, idlePauseTickBudget)
	defer cancel()
	acquired, err := s.leadership.IsLeader(ctx)
	if err != nil {
		s.logger.Printf("idle-pause: leader lock error: %v", err)
		return
	}
	if !acquired {
		return
	}
	report, err := s.RunOnce(ctx)
	if err != nil {
		s.logger.Printf("idle-pause: sweep failed: %v", err)
		return
	}
	if len(report.Warned)+len(report.Paused)+len(report.Failed) > 0 {
		s.logger.Printf("idle-pause: warned=%d paused=%d failed=%d", len(report.Warned), len(report.Paused), len(report.Failed))
	}
}

// RunOnce sweeps every project once, without touching the leader lock.
// Per-project failures land in the report; only a failed listing is an error.
func (s *IdlePauseScheduler) RunOnce(ctx context.Context) (IdlePauseReport, error) {
	var report IdlePauseReport
	instances, err := s.instances.FindAll()
	if err != nil {
		return report, fmt.Errorf("list instances: %w", err)
	}
	activity, err := s.activity.ListProjectActivity(ctx)
	if err != nil {
		return report, fmt.Errorf("list activity: %w", err)
	}
	now := s.now()
	for _, inst := range instances {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		row, seen := activity[inst.ProjectID]
		s.sweepOne(ctx, inst, row, seen, now, &report)
	}
	return report, nil
}

// sweepOne applies the pause / warn / skip decision to a single project.
func (s *IdlePauseScheduler) sweepOne(ctx context.Context, inst *domain.DatabaseInstance, row domain.ProjectActivity, seen bool, now time.Time, report *IdlePauseReport) {
	pauseAfterDays, ok := s.autoPauseDays(ctx, inst)
	if !ok {
		return
	}
	if domain.IsNotServable(inst.Status) {
		// A teardown owns it, or its restore was never confirmed. Neither is
		// a project the sweep may pause.
		return
	}
	if inst.Status == string(domain.StatusPausing) {
		s.retryStuckPause(ctx, inst, now, report)
		return
	}
	if inst.Status == string(domain.StatusResuming) {
		s.retryStuckResume(ctx, inst, now, report)
		return
	}
	if inst.Status != "ACTIVE" || s.resumedRecently(inst, now) {
		return
	}
	lastSeen, ok := effectiveLastSeen(inst, row, seen)
	if !ok {
		return
	}
	idle := now.Sub(lastSeen)
	switch {
	case idle >= time.Duration(pauseAfterDays)*idleDay:
		s.pause(ctx, inst, idle, report)
	case idle >= time.Duration(pauseAfterDays-1)*idleDay && warningDue(row, seen, lastSeen):
		s.warn(ctx, inst, lastSeen, lastSeen.Add(time.Duration(pauseAfterDays)*idleDay), now, report)
	}
}

// retryStuckPause drives a project whose pause failed part way back towards
// PAUSED. Left alone it would sit in PAUSING forever: the sweep's ordinary
// path only looks at ACTIVE projects, and nothing else calls pause for it.
func (s *IdlePauseScheduler) retryStuckPause(ctx context.Context, inst *domain.DatabaseInstance, now time.Time, report *IdlePauseReport) {
	if !pauseRetryDue(inst, now) {
		return
	}
	attempts, err := s.instances.RecordPauseAttempt(inst.ProjectID, now)
	if err != nil {
		s.logger.Printf("idle-pause: count retry for %s: %v", inst.ProjectID, err)
		return
	}
	s.logger.Printf("idle-pause: retrying the stuck pause of %s (attempt %d; next in %v if it fails)",
		inst.ProjectID, attempts, pauseRetryBackoffFor(attempts))
	reason := inst.PauseReason
	if reason == "" {
		reason = domain.PauseReasonIdle
	}
	if err := s.pauser.Pause(ctx, inst.ProjectID, reason); err != nil {
		s.logger.Printf("idle-pause: retry of %s failed: %v", inst.ProjectID, err)
		report.Failed = append(report.Failed, inst.ProjectID)
		return
	}
	s.logAudit(ctx, AuditActionIdlePause, inst.ProjectID, "stuck pause retried to completion")
	report.Paused = append(report.Paused, inst.ProjectID)
}

// retryStuckResume drives a project whose resume stopped part way back to
// ACTIVE, on the same persisted backoff a stuck pause uses. Left alone it
// would sit in RESUMING indefinitely with its database running and no CDC —
// a degraded tenant nobody is told about.
func (s *IdlePauseScheduler) retryStuckResume(ctx context.Context, inst *domain.DatabaseInstance, now time.Time, report *IdlePauseReport) {
	if s.resumer == nil || !pauseRetryDue(inst, now) {
		return
	}
	attempts, err := s.instances.RecordPauseAttempt(inst.ProjectID, now)
	if err != nil {
		s.logger.Printf("idle-pause: count resume retry for %s: %v", inst.ProjectID, err)
		return
	}
	s.logger.Printf("idle-pause: retrying the stuck resume of %s (attempt %d; next in %v if it fails)",
		inst.ProjectID, attempts, pauseRetryBackoffFor(attempts))
	if err := s.resumer.Resume(ctx, inst.ProjectID); err != nil {
		s.logger.Printf("idle-pause: resume retry of %s failed: %v", inst.ProjectID, err)
		report.Failed = append(report.Failed, inst.ProjectID)
		return
	}
	report.Resumed = append(report.Resumed, inst.ProjectID)
}

// autoPauseDays resolves the tier threshold; false when the tier is unknown
// or auto-pause is off for it.
func (s *IdlePauseScheduler) autoPauseDays(ctx context.Context, inst *domain.DatabaseInstance) (int, bool) {
	tc, err := s.tiers(ctx, inst.Tier)
	if err != nil || tc.AutoPauseAfterDays <= 0 {
		return 0, false
	}
	return tc.AutoPauseAfterDays, true
}

func (s *IdlePauseScheduler) resumedRecently(inst *domain.DatabaseInstance, now time.Time) bool {
	return inst.LastActiveAt != nil && now.Sub(inst.LastActiveAt.Time) < s.resumeGrace
}

// effectiveLastSeen is the latest of the activity marker, created_at and
// the last resume. False when none is known — such a project is skipped
// rather than treated as infinitely idle.
func effectiveLastSeen(inst *domain.DatabaseInstance, row domain.ProjectActivity, seen bool) (time.Time, bool) {
	var latest time.Time
	if seen {
		latest = row.LastSeenAt
	}
	if inst.CreatedAt != nil && inst.CreatedAt.Time.After(latest) {
		latest = inst.CreatedAt.Time
	}
	if inst.LastActiveAt != nil && inst.LastActiveAt.Time.After(latest) {
		latest = inst.LastActiveAt.Time
	}
	return latest, !latest.IsZero()
}

// warningDue is true when no warning covers the current idle stretch: none
// recorded, or one recorded before the project was last seen (stale).
func warningDue(row domain.ProjectActivity, seen bool, lastSeen time.Time) bool {
	return !seen || row.IdleWarnedAt == nil || row.IdleWarnedAt.Before(lastSeen)
}

func (s *IdlePauseScheduler) pause(ctx context.Context, inst *domain.DatabaseInstance, idle time.Duration, report *IdlePauseReport) {
	if err := s.pauser.Pause(ctx, inst.ProjectID, domain.PauseReasonIdle); err != nil {
		s.logger.Printf("idle-pause: pause %s failed: %v", inst.ProjectID, err)
		report.Failed = append(report.Failed, inst.ProjectID)
		return
	}
	s.logAudit(ctx, AuditActionIdlePause, inst.ProjectID, fmt.Sprintf("paused after %.1f idle days", idle.Hours()/24))
	report.Paused = append(report.Paused, inst.ProjectID)
}

func (s *IdlePauseScheduler) warn(ctx context.Context, inst *domain.DatabaseInstance, lastSeen, pauseAt, now time.Time, report *IdlePauseReport) {
	if err := s.activity.MarkIdleWarned(ctx, inst.ProjectID, lastSeen, now); err != nil {
		s.logger.Printf("idle-pause: record warning for %s failed: %v", inst.ProjectID, err)
		report.Failed = append(report.Failed, inst.ProjectID)
		return
	}
	s.logAudit(ctx, AuditActionIdleWarning, inst.ProjectID, "idle warning issued; auto-pause at "+pauseAt.UTC().Format(time.RFC3339))
	if s.notifier != nil {
		if err := s.notifier.NotifyIdleWarning(ctx, inst, pauseAt); err != nil {
			s.logger.Printf("idle-pause: warning delivery for %s failed (audit + activity row still record it): %v", inst.ProjectID, err)
		}
	}
	report.Warned = append(report.Warned, inst.ProjectID)
}

func (s *IdlePauseScheduler) logAudit(ctx context.Context, action, projectID, details string) {
	if s.audit == nil {
		return
	}
	ts := s.now()
	entry := &domain.AuditEntry{Action: action, Resource: "project", ResourceID: projectID, Details: details, Timestamp: &ts}
	if err := s.audit.LogAudit(ctx, entry); err != nil {
		s.logger.Printf("idle-pause: audit %s for %s failed: %v", action, projectID, err)
	}
}
