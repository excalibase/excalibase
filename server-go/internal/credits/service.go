package credits

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MinReasonLength = 8
	MaxReasonLength = 500
	// recentEntryCount is how many ledger lines the org's credit page opens with.
	recentEntryCount = 20
	maxEntriesPage   = 100
	sweepBatch       = 50
	adminAuditAction = "admin.credit_adjust"
)

var (
	ErrReasonLength = fmt.Errorf("a written reason of %d to %d characters is required", MinReasonLength, MaxReasonLength)
	ErrAboveCap     = errors.New("credits: the amount is above the cap for one admin action")
	ErrTermsVersion = errors.New("credits: these are not the current credit terms")
	ErrPastExpiry   = errors.New("credits: the expiry is not in the future")
	ErrMissingKey   = errors.New("credits: an idempotency key is required")
)

// Service is the credit rules on top of the ledger.
type Service struct {
	ledger Ledger
	rules  Rules
	now    func() time.Time
}

func NewService(ledger Ledger, rules Rules, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{ledger: ledger, rules: rules, now: now}
}

func (s *Service) Rules() Rules { return s.rules }

// Overview is everything the org's credit page and Home widget show.
type Overview struct {
	Account
	Entries      []Entry    `json:"entries"`
	NextExpiry   *time.Time `json:"nextExpiry"`
	Trial        TrialView  `json:"trial"`
	BilledIn     string     `json:"billedIn"`
	TermsVersion string     `json:"termsVersion"`
}

// TrialView is the signed-in person's sign-up credit: what it is, and where they stand.
type TrialView struct {
	State       string `json:"state"`
	Position    int    `json:"position,omitempty"`
	AmountMicro Micros `json:"amountMicros"`
	Days        int    `json:"days"`
}

func (s *Service) Overview(ctx context.Context, orgID, userID string) (Overview, error) {
	account, err := s.ledger.CreditAccount(ctx, orgID)
	if err != nil {
		return Overview{}, err
	}
	entries, err := s.ledger.CreditEntries(ctx, orgID, 0, recentEntryCount)
	if err != nil {
		return Overview{}, err
	}
	trial, err := s.trialFor(ctx, userID)
	if err != nil {
		return Overview{}, err
	}
	return Overview{Account: account, Entries: entries, NextExpiry: nextExpiry(account.Lots), Trial: trial, BilledIn: "UTC", TermsVersion: s.rules.TermsVersion}, nil
}

func nextExpiry(lots []Lot) *time.Time {
	var soonest *time.Time
	for _, lot := range lots {
		if lot.ExpiresAt != nil && (soonest == nil || lot.ExpiresAt.Before(*soonest)) {
			soonest = lot.ExpiresAt
		}
	}
	return soonest
}

func (s *Service) trialFor(ctx context.Context, userID string) (TrialView, error) {
	view := TrialView{State: "none", AmountMicro: s.rules.TrialAmount, Days: s.rules.TrialDays}
	state, err := s.ledger.CreditTrialState(ctx, userID)
	if err != nil {
		return view, err
	}
	view.State, view.Position = state.State, state.Position
	if state.State != "none" {
		return view, nil
	}
	accepted, err := s.ledger.CreditTermsVersion(ctx, userID)
	if err != nil {
		return view, err
	}
	if accepted != s.rules.TermsVersion {
		view.State = "needs_terms"
	} else {
		view.State = "pending"
	}
	return view, nil
}

// Entries pages an org's ledger, newest first.
func (s *Service) Entries(ctx context.Context, orgID string, beforeID int64, limit int) ([]Entry, error) {
	if limit <= 0 || limit > maxEntriesPage {
		limit = recentEntryCount
	}
	return s.ledger.CreditEntries(ctx, orgID, beforeID, limit)
}

func (s *Service) Statement(ctx context.Context, orgID string, month time.Time) (Statement, error) {
	return s.ledger.CreditStatement(ctx, orgID, month)
}

// AcceptTerms records that the person accepted the current credit terms, then
// tries the grant at once.
func (s *Service) AcceptTerms(ctx context.Context, userID, version string) (GrantResult, error) {
	if version != s.rules.TermsVersion {
		return GrantResult{}, ErrTermsVersion
	}
	if err := s.ledger.AcceptCreditTerms(ctx, userID, version, s.now()); err != nil {
		return GrantResult{}, err
	}
	return s.GrantTrial(ctx, userID)
}

// GrantTrial is the one door every verification path and the sweeper use.
func (s *Service) GrantTrial(ctx context.Context, userID string) (GrantResult, error) {
	return s.ledger.GrantCreditTrial(ctx, userID, s.rules, s.now())
}

// AdminChange is an admin's grant (positive) or adjustment (negative).
type AdminChange struct {
	OrgID          string
	ActorID        string
	AmountMicros   Micros
	Reason         string
	IdempotencyKey string
	ExpiresAt      *time.Time
	IPAddress      string
}

// Adjust applies an admin change after the checks every one must pass. The
// audit row is written by the ledger in the same transaction.
func (s *Service) Adjust(ctx context.Context, change AdminChange) (Entry, error) {
	reason := strings.TrimSpace(change.Reason)
	if n := utf8.RuneCountInString(reason); n < MinReasonLength || n > MaxReasonLength {
		return Entry{}, ErrReasonLength
	}
	if change.AmountMicros == 0 {
		return Entry{}, ErrInvalidAmount
	}
	if abs(change.AmountMicros) > s.rules.AdminCap {
		return Entry{}, ErrAboveCap
	}
	if change.IdempotencyKey == "" {
		return Entry{}, ErrMissingKey
	}
	now := s.now()
	if change.ExpiresAt != nil && !change.ExpiresAt.After(now) {
		return Entry{}, ErrPastExpiry
	}
	exists, err := s.ledger.CreditOrgExists(ctx, change.OrgID)
	if err != nil {
		return Entry{}, err
	}
	if !exists {
		return Entry{}, ErrOrgNotFound
	}
	return s.ledger.AdjustCredit(ctx, Adjustment{
		OrgID: change.OrgID, Amount: change.AmountMicros, Actor: change.ActorID, Reason: reason,
		IdempotencyKey: change.IdempotencyKey, ExpiresAt: change.ExpiresAt,
		Audit: AuditRecord{
			UserID: change.ActorID, Action: adminAuditAction, Resource: "org", ResourceID: change.OrgID, IPAddress: change.IPAddress,
			Details: fmt.Sprintf("amount_micros=%d reason=%q key=%s", int64(change.AmountMicros), reason, change.IdempotencyKey),
		},
	}, now)
}

func abs(value Micros) Micros {
	if value < 0 {
		return -value
	}
	return value
}

// Debit charges usage for a billing period against the lots good at its end.
func (s *Service) Debit(ctx context.Context, request DebitRequest) (DebitResult, error) {
	return s.ledger.DebitCredit(ctx, request)
}

// MeterWatermark is how far the usage meter has billed an org; the meter
// advances it with AdvanceMeterWatermark.
func (s *Service) MeterWatermark(ctx context.Context, orgID string) (time.Time, bool, error) {
	return s.ledger.CreditMeterWatermark(ctx, orgID)
}

func (s *Service) AdvanceMeterWatermark(ctx context.Context, orgID string, to time.Time) error {
	return s.ledger.AdvanceCreditMeterWatermark(ctx, orgID, to)
}

// Sweep expires lots the meter has passed, grants the sign-up credit to people
// whose verification no direct call caught, and serves the queue in order.
func (s *Service) Sweep(ctx context.Context) {
	if _, err := s.ledger.ExpireCreditLots(ctx, s.now(), s.rules.Metered); err != nil {
		log.Printf("credits sweep: expire: %v", err)
	}
	pending, err := s.ledger.PendingCreditTrialUsers(ctx, s.rules.TermsVersion, sweepBatch)
	if err != nil {
		log.Printf("credits sweep: pending users: %v", err)
	}
	s.grantAll(ctx, pending, false)
	queue, err := s.ledger.CreditWaitlist(ctx, sweepBatch)
	if err != nil {
		log.Printf("credits sweep: waitlist: %v", err)
	}
	s.grantAll(ctx, queue, true)
}

// grantAll tries each person in order. Serving the queue stops at the first
// person who still must wait; new arrivals all keep going so each one is queued.
func (s *Service) grantAll(ctx context.Context, userIDs []string, stopWhenFull bool) {
	for _, userID := range userIDs {
		result, err := s.GrantTrial(ctx, userID)
		if err != nil {
			log.Printf("credits sweep: grant %s: %v", userID, err)
			continue
		}
		if stopWhenFull && result.Outcome == OutcomeWaitlisted {
			return
		}
	}
}

// Leader says whether this replica is the one that runs the sweep.
type Leader interface {
	IsLeader(ctx context.Context) (bool, error)
}

// StartSweeper runs Sweep every interval on the leading replica and returns
// the function that stops it.
func (s *Service) StartSweeper(ctx context.Context, leader Leader, every time.Duration) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			s.sweepIfLeader(ctx, leader)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return cancel
}

func (s *Service) sweepIfLeader(ctx context.Context, leader Leader) {
	leading, err := leader.IsLeader(ctx)
	if err != nil {
		log.Printf("credits sweep: leadership check: %v", err)
		return
	}
	if leading {
		s.Sweep(ctx)
	}
}

// RecordTerms stores that the person accepted the current terms, for a person
// who ticked the box at sign-up. It grants nothing: the grant waits for the
// verified address.
func (s *Service) RecordTerms(ctx context.Context, userID, version string) error {
	if version != s.rules.TermsVersion {
		return ErrTermsVersion
	}
	return s.ledger.AcceptCreditTerms(ctx, userID, version, s.now())
}
