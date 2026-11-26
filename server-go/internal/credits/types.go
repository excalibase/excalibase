package credits

import (
	"errors"
	"time"
)

type EntryKind string

const (
	KindGrantTrial  EntryKind = "grant_trial"
	KindGrantAdmin  EntryKind = "grant_admin"
	KindAdjustAdmin EntryKind = "adjust_admin"
	KindUsage       EntryKind = "usage"
	KindExpire      EntryKind = "expire"
)

// Lot is a block of credit that expires together.
type Lot struct {
	ID        int64      `json:"id"`
	Kind      EntryKind  `json:"kind"`
	Original  Micros     `json:"originalMicros"`
	Remaining Micros     `json:"remainingMicros"`
	ExpiresAt *time.Time `json:"expiresAt"`
	CreatedAt time.Time  `json:"createdAt"`
}

// Entry is one append-only ledger line.
type Entry struct {
	ID          int64          `json:"id"`
	OrgID       string         `json:"orgId"`
	Kind        EntryKind      `json:"kind"`
	Amount      Micros         `json:"amountMicros"`
	Key         string         `json:"-"`
	PeriodStart *time.Time     `json:"periodStart,omitempty"`
	PeriodEnd   *time.Time     `json:"periodEnd,omitempty"`
	Actor       string         `json:"actor"`
	Reason      string         `json:"reason"`
	Details     map[string]any `json:"details"`
	CreatedAt   time.Time      `json:"createdAt"`
}

// Account is an org's balance with the lots that make it up.
type Account struct {
	OrgID   string `json:"orgId"`
	Balance Micros `json:"balanceMicros"`
	Lots    []Lot  `json:"lots"`
}

var (
	ErrInsufficientBalance = errors.New("credits: the adjustment is larger than the balance")
	ErrIdempotencyConflict = errors.New("credits: the idempotency key was used for a different request")
	ErrOrgNotFound         = errors.New("credits: org not found")
	ErrUserNotFound        = errors.New("credits: user not found")
	ErrInvalidAmount       = errors.New("credits: invalid amount")
)
