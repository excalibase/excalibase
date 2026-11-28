// Package creditzero is what happens to an org whose credit is used up or expired
// (EXC-866 C.5). Creates and growth are refused at once by the plan budget gate.
// This package does the rest: after a grace period it pauses compute above the
// plan's free allowance, newest first, remembers exactly what it paused, and
// resumes exactly that when credit returns. Disk is never touched.
package creditzero

import (
	"context"
	"time"
)

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
	// Replicas is what an app runs, or the copies of a database.
	Replicas int
}

// Holdings is the compute an org reserves, and the services of it that can be paused.
type Holdings struct {
	// CPUMilli and MemoryBytes are everything the org reserves, including what
	// cannot be paused (a functions runtime, a service still starting).
	CPUMilli    int64
	MemoryBytes int64
	Pausable    []Service
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
	Kind             string
	ProjectID        string
	ServiceID        string
	Name             string
	PreviousReplicas int
	PausedAt         time.Time
	// State is read from the live service, not stored.
	State Hold
}

// Key identifies the row.
func (p Paused) Key() string { return p.Kind + "/" + p.ProjectID + "/" + p.ServiceID }

// Store is what the enforcer reads and writes. Every time in it is the database clock.
type Store interface {
	Now(ctx context.Context) (time.Time, error)
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
	Pause(ctx context.Context, row Paused) error
	Resume(ctx context.Context, row Paused) error
}

// Allowance is the CPU and memory a plan never charges for; ok is false for a plan with none.
type Allowance func(tier string) (cpuMilli, memoryBytes int64, ok bool)
