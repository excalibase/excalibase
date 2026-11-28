package creditzero

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"time"
)

// Enforcer pauses and releases services for credit. Run it on the leading replica only.
type Enforcer struct {
	store     Store
	work      Workloads
	allowance Allowance
	grace     time.Duration
}

func New(store Store, work Workloads, allowance Allowance, grace time.Duration) *Enforcer {
	return &Enforcer{store: store, work: work, allowance: allowance, grace: grace}
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
	if now.Before(since.Add(e.grace)) {
		return e.dropStaleRows(ctx, org)
	}
	return errors.Join(e.dropStaleRows(ctx, org), e.pauseAboveAllowance(ctx, org))
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
// the plan's free allowance.
func (e *Enforcer) pauseAboveAllowance(ctx context.Context, org OrgState) error {
	cpuAllowed, memoryAllowed, ok := e.allowance(org.Tier)
	if !ok {
		return nil
	}
	held, err := e.store.Holdings(ctx, org.OrgID)
	if err != nil {
		return fmt.Errorf("org %s: read what it reserves: %w", org.OrgID, err)
	}
	candidates := slices.Clone(held.Pausable)
	slices.SortFunc(candidates, newestFirst)
	var failures []error
	for _, service := range candidates {
		if held.CPUMilli <= cpuAllowed && held.MemoryBytes <= memoryAllowed {
			break
		}
		if err := e.pauseOne(ctx, org.OrgID, service); err != nil {
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
// nobody remembers.
func (e *Enforcer) pauseOne(ctx context.Context, orgID string, service Service) error {
	row := Paused{Kind: service.Kind, ProjectID: service.ProjectID, ServiceID: service.ServiceID, Name: service.Name, PreviousReplicas: service.Replicas}
	if err := e.store.RecordPause(ctx, orgID, row); err != nil {
		return err
	}
	if err := e.work.Pause(ctx, row); err != nil {
		if forgetErr := e.store.ForgetPause(ctx, orgID, row); forgetErr != nil {
			log.Printf("ERROR: credit pause of %s failed and its row could not be removed: %v", row.Key(), forgetErr)
		}
		return err
	}
	return nil
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
