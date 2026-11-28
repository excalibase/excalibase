// Package creditzero is what happens to an org whose credit is used up or expired
// (EXC-866 C.5). Creates and growth are refused at once by the plan budget gate.
// This package does the rest: after a grace period it pauses compute above the
// plan's free allowance, newest first, remembers exactly what it paused, and
// resumes exactly that when credit returns. Disk is never touched.
package creditzero

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrNotCredits is a pause that found nothing to pause: the service was not
// running, or the org is no longer due. Nothing was changed, so no row is kept.
var ErrNotCredits = errors.New("not a service for credit to pause")

// ErrNoLongerDue is the check just before a pause finding the org no longer at $0
// past its grace, or its owner suspended.
var ErrNoLongerDue = fmt.Errorf("%w: the org is no longer due a credit pause", ErrNotCredits)

// Kinds of service the credit pauses.
const (
	KindDatabase = "database"
	KindApp      = "app"
)

// OrgState is where one org stands with its credit.
type OrgState struct {
	OrgID string
	Tier  string
	// AtZero is true for a FREE org whose credit balance is 0.
	AtZero bool
	// Since is when the credit ran out, nil until the first pass that saw it.
	Since *time.Time
	// OwnerSuspended is true while the org's owner is abuse-suspended.
	OwnerSuspended bool
}

// Service is a running service the credit could pause.
type Service struct {
	Kind      string
	ProjectID string
	ServiceID string
	Name      string
	CPUMilli  int64
	// MemoryBytes is what the service reserves while it runs.
	MemoryBytes int64
	CreatedAt   time.Time
}

// Holdings is the compute an org reserves, and the services of it that can be paused.
type Holdings struct {
	// CPUMilli and MemoryBytes are everything the org reserves, including what
	// cannot be paused (a functions runtime, a service still starting).
	CPUMilli    int64
	MemoryBytes int64
	Pausable    []Service
	// Transitioning is true while a database or app of the org is starting,
	// stopping, resuming or deploying: its numbers are not settled, so nothing
	// is paused to make room until they are.
	Transitioning bool
}

// Hold says what became of a service the credit paused.
type Hold int

const (
	// HoldPaused: still paused for credit; resume it when credit returns.
	HoldPaused Hold = iota
	// HoldBusy: changing state right now; look again on the next pass.
	HoldBusy
	// HoldReleased: running again, or paused by the user for their own reason; not credit's to start.
	HoldReleased
	// HoldGone: deleted or being deleted.
	HoldGone
)

// Paused is one row of credit_paused_services.
type Paused struct {
	Kind      string
	ProjectID string
	ServiceID string
	Name      string
	PausedAt  time.Time
	// State is read from the live service, not stored.
	State Hold
}

// Key identifies the row.
func (p Paused) Key() string { return p.Kind + "/" + p.ProjectID + "/" + p.ServiceID }

// Store is what the enforcer reads and writes. Every time in it is the database clock.
type Store interface {
	Now(ctx context.Context) (time.Time, error)
	// Org reads one org's state again, for the check just before a pause.
	Org(ctx context.Context, orgID string) (OrgState, error)
	// Orgs lists every org that holds a credit account, a grace period or a paused service.
	Orgs(ctx context.Context) ([]OrgState, error)
	// MarkZero starts the org's grace period; it keeps the first date when one is already running.
	MarkZero(ctx context.Context, orgID string) error
	ClearZero(ctx context.Context, orgID string) error
	Holdings(ctx context.Context, orgID string) (Holdings, error)
	RecordPause(ctx context.Context, orgID string, row Paused) error
	ForgetPause(ctx context.Context, orgID string, row Paused) error
	Paused(ctx context.Context, orgID string) ([]Paused, error)
	// PurgeOrphans drops rows of orgs that no longer exist.
	PurgeOrphans(ctx context.Context) error
}

// Workloads pauses and resumes a service through the same lifecycle a person uses.
type Workloads interface {
	// Pause runs admitted under the service's own lease, just before it acts, and
	// stops if it fails. It returns ErrNotCredits when it found the service not
	// running, or not one to pause, and so changed nothing.
	Pause(ctx context.Context, row Paused, admitted func() error) error
	Resume(ctx context.Context, row Paused) error
}

// Allowance is the CPU and memory a plan never charges for; ok is false for a plan with none.
type Allowance func(tier string) (cpuMilli, memoryBytes int64, ok bool)
