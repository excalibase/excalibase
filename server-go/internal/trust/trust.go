// Package trust decides how much of the platform an account may use until it
// is verified (EXC-899): a new account is limited, a verified one is full.
// It only decides. Nothing here suspends, stops or renders anything, and it
// imports only domain and features so that stays true.
package trust

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// State is what an account may run.
type State string

const (
	Limited State = "limited"
	Full    State = "full"
)

// Method is how the state was reached.
type Method string

const (
	MethodGitHub        Method = "github"
	MethodGrandfathered Method = "grandfathered"
	MethodAdmin         Method = "admin"
)

// Reason is a code, never free text: Studio words it from the code and the
// current thresholds, so no GitHub data has to be kept to explain a decision.
type Reason string

const (
	ReasonUnverified        Reason = "unverified"
	ReasonGitHubTooNew      Reason = "github_too_new"
	ReasonGitHubInactive    Reason = "github_inactive"
	ReasonCheckFailed       Reason = "github_check_failed"
	ReasonGitHubAlreadyUsed Reason = "github_already_used"
	ReasonEmailUnverified   Reason = "email_unverified"
	ReasonOK                Reason = "ok"
	ReasonGrandfathered     Reason = "grandfathered"
	ReasonAdminOverride     Reason = "admin_override"
)

var (
	states  = map[State]bool{Limited: true, Full: true}
	methods = map[Method]bool{MethodGitHub: true, MethodGrandfathered: true, MethodAdmin: true}
	reasons = map[Reason]bool{
		ReasonUnverified: true, ReasonGitHubTooNew: true, ReasonGitHubInactive: true, ReasonCheckFailed: true,
		ReasonGitHubAlreadyUsed: true, ReasonEmailUnverified: true, ReasonOK: true, ReasonGrandfathered: true,
		ReasonAdminOverride: true,
	}
)

// ErrInvalidRecord refuses a record that names an unknown state, method or reason.
var ErrInvalidRecord = errors.New("invalid trust record")

// Record is the stored decision for one account. It holds the derived result
// only; no GitHub data is kept (EXC-899 privacy rule).
type Record struct {
	UserID string
	State  State
	Method Method
	Reason Reason
	// DecidedAt is set by the store when the decision is saved.
	DecidedAt time.Time
	// CheckVersion is the rule set that decided, so a rule change can be told apart.
	CheckVersion int
}

// Validate refuses anything outside the known states, methods and reasons.
func (r Record) Validate() error {
	switch {
	case r.UserID == "":
		return fmt.Errorf("%w: no user", ErrInvalidRecord)
	case !states[r.State]:
		return fmt.Errorf("%w: state %q", ErrInvalidRecord, r.State)
	case !methods[r.Method]:
		return fmt.Errorf("%w: method %q", ErrInvalidRecord, r.Method)
	case !reasons[r.Reason]:
		return fmt.Errorf("%w: reason %q", ErrInvalidRecord, r.Reason)
	}
	return nil
}

// Store keeps decisions. Save writes the row and its audit entry together.
type Store interface {
	// Find returns nil, nil when the account has no decision yet.
	Find(ctx context.Context, userID string) (*Record, error)
	// Save stores the decision and appends one audit entry naming the actor
	// ("system" or an admin's id). The actor is required.
	Save(ctx context.Context, record Record, actor string) error
}
