package credits

import (
	"context"
	"time"
)

// Rules are the numbers every grant and adjustment follows; all of them are
// configuration.
type Rules struct {
	TrialAmount  Micros
	TrialDays    int
	TrialCap     int
	AdminCap     Micros
	TermsVersion string
	// Metered is true once a usage meter runs: lots then wait for each org's meter watermark.
	Metered bool
}

// GrantOutcome says what a grant attempt did.
type GrantOutcome string

const (
	OutcomeGranted         GrantOutcome = "granted"
	OutcomeAlreadyGranted  GrantOutcome = "already_granted"
	OutcomeWaitlisted      GrantOutcome = "waitlisted"
	OutcomeNeedsTerms      GrantOutcome = "needs_terms"
	OutcomeNotEligible     GrantOutcome = "not_eligible"
	OutcomeIdentityClaimed GrantOutcome = "identity_claimed"
	OutcomeNoFreeOrg       GrantOutcome = "no_free_org"
)

// GrantResult is the outcome and, when credit landed, where.
type GrantResult struct {
	Outcome GrantOutcome
	OrgID   string
	EntryID int64
}

// DebitRequest is one usage charge for a billing period (UTC).
type DebitRequest struct {
	OrgID          string
	Amount         Micros
	PeriodStart    time.Time
	PeriodEnd      time.Time
	IdempotencyKey string
	Details        map[string]any
}

// DebitResult is what a debit took. Uncovered is what the lots could not pay;
// it is recorded on the entry and never makes the balance negative.
type DebitResult struct {
	EntryID   int64
	Taken     Micros
	Uncovered Micros
	Replayed  bool
}

// AuditRecord is the audit row an admin action writes in the same transaction
// as its ledger entry.
type AuditRecord struct {
	UserID     string
	Action     string
	Resource   string
	ResourceID string
	Details    string
	IPAddress  string
}

// Adjustment is an admin grant (positive) or adjustment (negative).
type Adjustment struct {
	OrgID          string
	Amount         Micros
	Actor          string
	Reason         string
	IdempotencyKey string
	ExpiresAt      *time.Time
	Audit          AuditRecord
}

// Statement is one UTC month of an org's ledger.
type Statement struct {
	Month   string  `json:"month"`
	Opening Micros  `json:"openingMicros"`
	Closing Micros  `json:"closingMicros"`
	Entries []Entry `json:"entries"`
}

// TrialState is where a person stands with the sign-up credit.
type TrialState struct {
	State    string `json:"state"`
	Position int    `json:"position,omitempty"`
}

// Ledger is the credit store. Every method that changes money runs in one
// transaction.
type Ledger interface {
	CreditAccount(ctx context.Context, orgID string) (Account, error)
	CreditEntries(ctx context.Context, orgID string, beforeID int64, limit int) ([]Entry, error)
	CreditStatement(ctx context.Context, orgID string, month time.Time) (Statement, error)
	// CreditUsageBurn is what usage drew per UTC day since `from`.
	CreditUsageBurn(ctx context.Context, orgID string, from time.Time) (UsageBurn, error)
	DebitCredit(ctx context.Context, request DebitRequest) (DebitResult, error)
	AdjustCredit(ctx context.Context, adjustment Adjustment, now time.Time) (Entry, error)
	// ExpireCreditLots ends lots whose date has passed. With requireWatermark a lot also waits for its
	// org's meter watermark to reach the date; without it the clock alone decides.
	ExpireCreditLots(ctx context.Context, now time.Time, requireWatermark bool) (int, error)
	CreditMeterWatermark(ctx context.Context, orgID string) (time.Time, bool, error)
	AdvanceCreditMeterWatermark(ctx context.Context, orgID string, to time.Time) error

	AcceptCreditTerms(ctx context.Context, userID, version string, now time.Time) error
	CreditTermsVersion(ctx context.Context, userID string) (string, error)
	GrantCreditTrial(ctx context.Context, userID string, rules Rules, now time.Time) (GrantResult, error)
	PendingCreditTrialUsers(ctx context.Context, termsVersion string, limit int) ([]string, error)
	CreditWaitlist(ctx context.Context, limit int) ([]string, error)
	CreditTrialState(ctx context.Context, userID string) (TrialState, error)
	CreditOrgExists(ctx context.Context, orgID string) (bool, error)
}
