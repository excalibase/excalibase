package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/credits"
)

// creditGrantLockSpace serialises sign-up grants, so the trial cap is counted
// against every grant already committed.
const creditGrantLockSpace = 866

const creditTrialKeyFormat = "grant_trial:user:%s"

func (s *Store) AcceptCreditTerms(ctx context.Context, userID, version string, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO credit_terms_acceptances (user_id, terms_version, accepted_at) VALUES ($1, $2, $3)
		 ON CONFLICT (user_id) DO UPDATE SET terms_version = EXCLUDED.terms_version, accepted_at = EXCLUDED.accepted_at`,
		userID, version, now.UTC())
	return err
}

// CreditTermsVersion is the terms version the person accepted, or "".
func (s *Store) CreditTermsVersion(ctx context.Context, userID string) (string, error) {
	var version string
	err := s.db.QueryRowContext(ctx, `SELECT terms_version FROM credit_terms_acceptances WHERE user_id = $1`, userID).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return version, err
}

type grantCandidate struct {
	verified, active bool
	email            string
	acceptedVersion  string
	acceptedAt       time.Time
}

func loadGrantCandidate(ctx context.Context, tx *sql.Tx, userID string) (grantCandidate, error) {
	var candidate grantCandidate
	var accepted sql.NullTime
	var version sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT u.email_verified_at IS NOT NULL,
		        u.active AND COALESCE(NULLIF(u.kind, ''), 'human') = 'human' AND NOT EXISTS (SELECT 1 FROM abuse_suspensions a WHERE a.user_id = u.id),
		        lower(u.email), t.terms_version, t.accepted_at
		 FROM users u LEFT JOIN credit_terms_acceptances t ON t.user_id = u.id WHERE u.id = $1`, userID).
		Scan(&candidate.verified, &candidate.active, &candidate.email, &version, &accepted)
	if errors.Is(err, sql.ErrNoRows) {
		return candidate, credits.ErrUserNotFound
	}
	candidate.acceptedVersion, candidate.acceptedAt = version.String, accepted.Time
	return candidate, err
}

// grantPlan is what the checks before a grant found.
type grantPlan struct {
	// refusal is the outcome when the person cannot be granted now; empty when they can.
	refusal    credits.GrantOutcome
	orgID      string
	identities []claimKey
}

func (s *Store) GrantCreditTrial(ctx context.Context, userID string, rules credits.Rules, now time.Time) (credits.GrantResult, error) {
	result := credits.GrantResult{Outcome: credits.OutcomeNotEligible}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, 0)`, creditGrantLockSpace); err != nil {
		return result, err
	}
	candidate, err := loadGrantCandidate(ctx, tx, userID)
	if errors.Is(err, credits.ErrUserNotFound) {
		return result, errors.Join(err, leaveCreditQueue(ctx, tx, userID), tx.Commit())
	}
	if err != nil {
		return result, err
	}
	plan, err := planGrant(ctx, tx, userID, rules, candidate)
	if err != nil {
		return result, err
	}
	if plan.refusal != "" {
		// Whoever cannot be granted leaves the queue, so they never hold a place a newcomer is owed.
		if plan.refusal == credits.OutcomeIdentityClaimed {
			err = recordRefusedGrant(ctx, tx, userID, plan.orgID, now)
		}
		if err == nil {
			err = leaveCreditQueue(ctx, tx, userID)
		}
		return credits.GrantResult{Outcome: plan.refusal, OrgID: plan.orgID}, commitUnless(tx, err)
	}
	result.OrgID = plan.orgID
	if queued, err := mustWait(ctx, tx, userID, rules, now); err != nil || queued {
		result.Outcome = credits.OutcomeWaitlisted
		return result, commitUnless(tx, err)
	}
	entryID, err := writeTrialGrant(ctx, tx, userID, plan.orgID, rules, candidate, plan.identities, now)
	if err != nil {
		return result, err
	}
	if err := leaveCreditQueue(ctx, tx, userID); err != nil {
		return result, err
	}
	result.Outcome, result.EntryID = credits.OutcomeGranted, entryID
	return result, tx.Commit()
}

func planGrant(ctx context.Context, tx *sql.Tx, userID string, rules credits.Rules, candidate grantCandidate) (grantPlan, error) {
	if early, done := candidate.verdict(rules); done {
		return grantPlan{refusal: early}, nil
	}
	if claimed, err := userClaimOutcome(ctx, tx, userID); err != nil || claimed != "" {
		return grantPlan{refusal: claimed}, err
	}
	orgID, err := freeOrgOf(ctx, tx, userID)
	if err != nil {
		return grantPlan{}, err
	}
	if orgID == "" {
		return grantPlan{refusal: credits.OutcomeNoFreeOrg}, nil
	}
	identities, err := linkedClaimableIdentities(ctx, tx, userID)
	if err != nil {
		return grantPlan{}, err
	}
	// The exact address, lower-cased and nothing else, so a deleted account's address cannot earn again.
	identities = append(identities, claimKey{"email", candidate.email})
	taken, err := identityAlreadyClaimed(ctx, tx, userID, identities)
	if err != nil || taken {
		return grantPlan{refusal: credits.OutcomeIdentityClaimed, orgID: orgID}, err
	}
	return grantPlan{orgID: orgID, identities: identities}, nil
}

func leaveCreditQueue(ctx context.Context, tx *sql.Tx, userID string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM credit_waitlist WHERE user_id = $1`, userID)
	return err
}

// commitUnless commits when nothing failed, so a refusal or a place in the
// queue is kept while an error leaves nothing behind.
func commitUnless(tx *sql.Tx, err error) error {
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (c grantCandidate) verdict(rules credits.Rules) (credits.GrantOutcome, bool) {
	switch {
	case !c.verified || !c.active:
		return credits.OutcomeNotEligible, true
	case c.acceptedVersion != rules.TermsVersion:
		return credits.OutcomeNeedsTerms, true
	}
	return "", false
}

// userClaimOutcome is what an earlier attempt already decided for the person.
func userClaimOutcome(ctx context.Context, tx *sql.Tx, userID string) (credits.GrantOutcome, error) {
	var entry sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT entry_id FROM credit_grant_claims WHERE claim_kind = 'user' AND claim_value = $1`, userID).Scan(&entry)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", err
	case entry.Valid:
		return credits.OutcomeAlreadyGranted, nil
	}
	return credits.OutcomeIdentityClaimed, nil
}

func freeOrgOf(ctx context.Context, tx *sql.Tx, userID string) (string, error) {
	var orgID string
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM orgs WHERE owner_id = $1 AND UPPER(tier) = 'FREE' ORDER BY created_at, id LIMIT 1`, userID).Scan(&orgID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return orgID, err
}

type claimKey struct{ kind, value string }

func linkedClaimableIdentities(ctx context.Context, tx *sql.Tx, userID string) ([]claimKey, error) {
	rows, err := tx.QueryContext(ctx, `SELECT provider, subject FROM studio_identities WHERE user_id = $1 AND provider IN ('github', 'google') ORDER BY provider, subject`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []claimKey
	for rows.Next() {
		var key claimKey
		if err := rows.Scan(&key.kind, &key.value); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func identityAlreadyClaimed(ctx context.Context, tx *sql.Tx, userID string, identities []claimKey) (bool, error) {
	for _, identity := range identities {
		var holder string
		err := tx.QueryRowContext(ctx, `SELECT user_id FROM credit_grant_claims WHERE claim_kind = $1 AND claim_value = $2`, identity.kind, identity.value).Scan(&holder)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil || holder != userID {
			return err == nil, err
		}
	}
	return false, nil
}

func recordRefusedGrant(ctx context.Context, tx *sql.Tx, userID, orgID string, now time.Time) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO credit_grant_claims (claim_kind, claim_value, user_id, org_id, at) VALUES ('user', $1, $1, $2, $3) ON CONFLICT DO NOTHING`, userID, orgID, now.UTC())
	return err
}

// eligibleAheadSQL counts the queued people ahead of one person who could take a place today. Someone who
// upgraded, was suspended or deactivated, lost their free org or never accepted the current terms is not
// counted, so they cannot hold up the people behind them.
const eligibleAheadSQL = `SELECT count(*) FROM credit_waitlist w
	JOIN users u ON u.id = w.user_id
	JOIN credit_terms_acceptances t ON t.user_id = u.id AND t.terms_version = $2
	WHERE w.seq < (SELECT seq FROM credit_waitlist WHERE user_id = $1)
	  AND u.email_verified_at IS NOT NULL AND u.active AND COALESCE(NULLIF(u.kind, ''), 'human') = 'human'
	  AND NOT EXISTS (SELECT 1 FROM abuse_suspensions a WHERE a.user_id = u.id)
	  AND EXISTS (SELECT 1 FROM orgs o WHERE o.owner_id = u.id AND UPPER(o.tier) = 'FREE')
	  AND NOT EXISTS (SELECT 1 FROM credit_grant_claims c WHERE c.claim_kind = 'user' AND c.claim_value = u.id)`

// mustWait puts the person in the queue when the cap is full or others who
// asked first still wait for the places that are free.
func mustWait(ctx context.Context, tx *sql.Tx, userID string, rules credits.Rules, now time.Time) (bool, error) {
	var holders, ahead int
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM credit_lots l JOIN credit_entries e ON e.id = l.source_entry_id
		 WHERE e.kind = 'grant_trial' AND l.remaining_micros > 0 AND (l.expires_at IS NULL OR l.expires_at > $1)`, now.UTC()).Scan(&holders); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO credit_waitlist (user_id, queued_at) VALUES ($1, $2) ON CONFLICT (user_id) DO NOTHING`, userID, now.UTC()); err != nil {
		return false, err
	}
	if err := tx.QueryRowContext(ctx, eligibleAheadSQL, userID, rules.TermsVersion).Scan(&ahead); err != nil {
		return false, err
	}
	return holders >= rules.TrialCap || ahead >= rules.TrialCap-holders, nil
}

func writeTrialGrant(ctx context.Context, tx *sql.Tx, userID, orgID string, rules credits.Rules, candidate grantCandidate, identities []claimKey, now time.Time) (int64, error) {
	if _, err := lockCreditAccount(ctx, tx, orgID, now); err != nil {
		return 0, err
	}
	expires := now.UTC().AddDate(0, 0, rules.TrialDays)
	entryID, err := insertCreditEntry(ctx, tx, newEntry{orgID: orgID, kind: credits.KindGrantTrial, amount: rules.TrialAmount,
		key: fmt.Sprintf(creditTrialKeyFormat, userID), actor: "system", reason: "sign-up credit",
		details: map[string]any{"termsVersion": candidate.acceptedVersion}, at: now})
	if err != nil {
		return 0, err
	}
	if err := insertCreditLot(ctx, tx, orgID, entryID, rules.TrialAmount, &expires, now); err != nil {
		return 0, err
	}
	if err := addToCreditBalance(ctx, tx, orgID, rules.TrialAmount, now); err != nil {
		return 0, err
	}
	claims := append([]claimKey{{"user", userID}}, identities...)
	for _, claim := range claims {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO credit_grant_claims (claim_kind, claim_value, user_id, org_id, entry_id, terms_version, terms_accepted_at, at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			claim.kind, claim.value, userID, orgID, entryID, candidate.acceptedVersion, candidate.acceptedAt.UTC(), now.UTC()); err != nil {
			return 0, err
		}
	}
	return entryID, nil
}

// PendingCreditTrialUsers are verified people with a free org who accepted the
// terms and neither hold a claim nor wait in the queue, oldest acceptance first.
func (s *Store) PendingCreditTrialUsers(ctx context.Context, termsVersion string, limit int) ([]string, error) {
	return s.userIDs(ctx,
		`SELECT t.user_id FROM credit_terms_acceptances t JOIN users u ON u.id = t.user_id
		 WHERE t.terms_version = $1 AND u.email_verified_at IS NOT NULL AND u.active
		   AND NOT EXISTS (SELECT 1 FROM abuse_suspensions a WHERE a.user_id = u.id)
		   AND EXISTS (SELECT 1 FROM orgs o WHERE o.owner_id = u.id AND UPPER(o.tier) = 'FREE')
		   AND NOT EXISTS (SELECT 1 FROM credit_grant_claims c WHERE c.claim_kind = 'user' AND c.claim_value = u.id)
		   AND NOT EXISTS (SELECT 1 FROM credit_waitlist w WHERE w.user_id = u.id)
		 ORDER BY t.accepted_at, t.user_id LIMIT $2`, termsVersion, limit)
}

// CreditWaitlist is the queue, first come first.
func (s *Store) CreditWaitlist(ctx context.Context, limit int) ([]string, error) {
	return s.userIDs(ctx, `SELECT user_id FROM credit_waitlist ORDER BY seq LIMIT $1`, limit)
}

func (s *Store) userIDs(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CreditTrialState is where the person stands: granted, waitlisted, refused or none.
func (s *Store) CreditTrialState(ctx context.Context, userID string) (credits.TrialState, error) {
	var entry sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT entry_id FROM credit_grant_claims WHERE claim_kind = 'user' AND claim_value = $1`, userID).Scan(&entry)
	switch {
	case err == nil && entry.Valid:
		return credits.TrialState{State: "granted"}, nil
	case err == nil:
		return credits.TrialState{State: "refused"}, nil
	case !errors.Is(err, sql.ErrNoRows):
		return credits.TrialState{}, err
	}
	var position int
	err = s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM credit_waitlist WHERE seq <= (SELECT seq FROM credit_waitlist WHERE user_id = $1)`, userID).Scan(&position)
	if err != nil || position == 0 {
		return credits.TrialState{State: "none"}, err
	}
	return credits.TrialState{State: "waitlisted", Position: position}, nil
}
