package creditzero

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/excalibase/provisioning-poc/internal/metrics"
)

// Enforcer pauses and releases services for credit. Run it on the leading replica only.
type Enforcer struct {
	store     Store
	work      Workloads
	allowance Allowance
	grace     time.Duration
	backoff   map[string]pauseBackoff
}

// pauseBackoff is how long a service that failed to pause is left alone.
type pauseBackoff struct {
	failures int
	retryAt  time.Time
}

const (
	creditPauseBackoffBase = time.Minute
	creditPauseBackoffMax  = time.Hour
)

func New(store Store, work Workloads, allowance Allowance, grace time.Duration) *Enforcer {
	return &Enforcer{store: store, work: work, allowance: allowance, grace: grace, backoff: map[string]pauseBackoff{}}
}

// Pass looks at every org once. A failure for one service is reported at the end
// and never stops the others.
func (e *Enforcer) Pass(ctx context.Context) error {
	now, err := e.store.Now(ctx)
	if err != nil {
		return fmt.Errorf("read the database clock: %w", err)
	}
	orgs, err := e.store.Orgs(ctx)
	if err != nil {
		return fmt.Errorf("list orgs: %w", err)
	}
	failures := []error{e.store.PurgeOrphans(ctx)}
	for _, org := range orgs {
		failures = append(failures, e.passOrg(ctx, org, now))
	}
	return errors.Join(failures...)
}

func (e *Enforcer) passOrg(ctx context.Context, org OrgState, now time.Time) error {
	if !org.AtZero {
		return e.release(ctx, org)
	}
	since := org.Since
	if since == nil {
		if err := e.store.MarkZero(ctx, org.OrgID); err != nil {
			return fmt.Errorf("org %s: start the grace period: %w", org.OrgID, err)
		}
		since = &now
	}
	stale := e.dropStaleRows(ctx, org)
	if org.OwnerSuspended || now.Before(since.Add(e.grace)) {
		return stale
	}
	return errors.Join(stale, e.pauseAboveAllowance(ctx, org, now))
}

// due reads the org again: at $0, past its grace, owner not suspended. The state a
// pass read at its start can be minutes old by the time a pause runs.
func (e *Enforcer) due(ctx context.Context, orgID string) error {
	now, err := e.store.Now(ctx)
	if err != nil {
		return fmt.Errorf("read the database clock: %w", err)
	}
	org, err := e.store.Org(ctx, orgID)
	if err != nil {
		return fmt.Errorf("read org %s again: %w", orgID, err)
	}
	if !org.AtZero || org.OwnerSuspended || org.Since == nil || now.Before(org.Since.Add(e.grace)) {
		return ErrNoLongerDue
	}
	return nil
}

// ReleaseAll resumes every service the credit paused and ends every grace period,
// whatever the balances: it is what turning the metering off does.
func (e *Enforcer) ReleaseAll(ctx context.Context) error {
	orgs, err := e.store.Orgs(ctx)
	if err != nil {
		return fmt.Errorf("list orgs: %w", err)
	}
	failures := []error{e.store.PurgeOrphans(ctx)}
	for _, org := range orgs {
		failures = append(failures, e.release(ctx, org))
	}
	return errors.Join(failures...)
}

// release ends the grace period and resumes what the credit paused. A suspended
// owner's services wait: the suspension outranks credit, and their unsuspend runs
// the same release on the next pass.
func (e *Enforcer) release(ctx context.Context, org OrgState) error {
	if org.Since != nil {
		if err := e.store.ClearZero(ctx, org.OrgID); err != nil {
			return fmt.Errorf("org %s: end the grace period: %w", org.OrgID, err)
		}
	}
	if org.OwnerSuspended {
		return nil
	}
	rows, err := e.store.Paused(ctx, org.OrgID)
	if err != nil {
		return fmt.Errorf("org %s: list paused services: %w", org.OrgID, err)
	}
	var failures []error
	for _, row := range rows {
		if err := e.releaseOne(ctx, org.OrgID, row); err != nil {
			failures = append(failures, fmt.Errorf("org %s: resume %s: %w", org.OrgID, row.Key(), err))
		}
	}
	return errors.Join(failures...)
}

func (e *Enforcer) releaseOne(ctx context.Context, orgID string, row Paused) error {
	switch row.State {
	case HoldBusy:
		return nil
	case HoldPaused:
		if err := e.work.Resume(ctx, row); err != nil {
			return err
		}
	}
	return e.store.ForgetPause(ctx, orgID, row)
}

// dropStaleRows forgets rows of services that are gone or that the user has since
// resumed or paused themselves, so credit never starts what is not its to start.
func (e *Enforcer) dropStaleRows(ctx context.Context, org OrgState) error {
	rows, err := e.store.Paused(ctx, org.OrgID)
	if err != nil {
		return fmt.Errorf("org %s: list paused services: %w", org.OrgID, err)
	}
	var failures []error
	for _, row := range rows {
		if row.State == HoldReleased || row.State == HoldGone {
			failures = append(failures, e.store.ForgetPause(ctx, org.OrgID, row))
		}
	}
	return errors.Join(failures...)
}

// pauseAboveAllowance pauses the newest services until CPU and memory both fit
// the plan's free allowance. An org with a service in transition waits: its
// numbers are not settled, and nothing is paused to make room for them.
func (e *Enforcer) pauseAboveAllowance(ctx context.Context, org OrgState, now time.Time) error {
	cpuAllowed, memoryAllowed, ok := e.allowance(org.Tier)
	if !ok {
		return nil
	}
	held, err := e.store.Holdings(ctx, org.OrgID)
	if err != nil {
		return fmt.Errorf("org %s: read what it reserves: %w", org.OrgID, err)
	}
	if held.Transitioning {
		return nil
	}
	candidates := slices.Clone(held.Pausable)
	slices.SortFunc(candidates, newestFirst)
	var failures []error
	for _, service := range candidates {
		if held.CPUMilli <= cpuAllowed && held.MemoryBytes <= memoryAllowed {
			break
		}
		err := e.pauseOne(ctx, org.OrgID, service, now)
		if errors.Is(err, ErrNoLongerDue) {
			break
		}
		if errors.Is(err, ErrNotCredits) {
			continue
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("org %s: pause %s: %w", org.OrgID, service.ServiceID, err))
			continue
		}
		held.CPUMilli -= service.CPUMilli
		held.MemoryBytes -= service.MemoryBytes
	}
	return errors.Join(failures...)
}

func newestFirst(a, b Service) int {
	if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
		return c
	}
	return -compareStrings(a.ServiceID, b.ServiceID)
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// pauseOne writes the row first and pauses second: a crash between the two leaves
// a row for a running service, which the next pass drops, never a paused service
// nobody remembers. A pause that fails keeps its row, since it may have stopped
// part of the service and be finished later by the retry that finishes any
// stuck pause; the next pass drops the row if the service is running after all.
// A pause that changed nothing, because the service was not running or the org
// is no longer due, loses its row.
func (e *Enforcer) pauseOne(ctx context.Context, orgID string, service Service, now time.Time) error {
	row := Paused{Kind: service.Kind, ProjectID: service.ProjectID, ServiceID: service.ServiceID, Name: service.Name}
	if waiting, ok := e.backoff[row.Key()]; ok && now.Before(waiting.retryAt) {
		return ErrNotCredits
	}
	if err := e.due(ctx, orgID); err != nil {
		return err
	}
	if err := e.store.RecordPause(ctx, orgID, row); err != nil {
		return err
	}
	err := e.work.Pause(ctx, row, func() error { return e.due(ctx, orgID) })
	switch {
	case err == nil:
		delete(e.backoff, row.Key())
		return nil
	case errors.Is(err, ErrNotCredits):
		if forgetErr := e.store.ForgetPause(ctx, orgID, row); forgetErr != nil {
			return errors.Join(err, forgetErr)
		}
		return err
	}
	e.backOff(row.Key(), now)
	metrics.CountCreditMeterEvent(metrics.CreditMeterPauseFailed)
	return err
}

// backOff makes a service that failed to pause wait twice as long after each failure.
func (e *Enforcer) backOff(key string, now time.Time) {
	failures := e.backoff[key].failures + 1
	wait := creditPauseBackoffBase << (failures - 1)
	if wait > creditPauseBackoffMax || wait <= 0 {
		wait = creditPauseBackoffMax
	}
	e.backoff[key] = pauseBackoff{failures: failures, retryAt: now.Add(wait)}
}

// Leader says whether this replica is the one that enforces.
type Leader interface {
	IsLeader(ctx context.Context) (bool, error)
}

// Tick is one round on the leading replica: enforce while credit is metered, and
// release everything while it is not, so turning the flag off lets every service run.
func (e *Enforcer) Tick(ctx context.Context, leader Leader, metered func(context.Context) bool) {
	leading, err := leader.IsLeader(ctx)
	if err != nil {
		log.Printf("credit zero: leadership check: %v", err)
		return
	}
	if !leading {
		return
	}
	pass := e.ReleaseAll
	if metered(ctx) {
		pass = e.Pass
	}
	if err := pass(ctx); err != nil {
		log.Printf("credit zero: %v", err)
	}
}

// Start runs Tick every interval and returns the function that stops it.
func (e *Enforcer) Start(ctx context.Context, leader Leader, every time.Duration, metered func(context.Context) bool) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			e.Tick(ctx, leader, metered)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return cancel
}
