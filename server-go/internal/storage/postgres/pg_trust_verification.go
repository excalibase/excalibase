package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/trust"
)

// verificationActor is the audit actor of a decision the account earned itself.
const verificationActor = "verify"

// Verifications adapts the store to trust.VerificationStore. A claim released
// by a deleted account stays closed to others for cooldownDays.
func (s *Store) Verifications(cooldownDays int) trust.VerificationStore {
	return verificationStore{s: s, cooldown: time.Duration(cooldownDays) * 24 * time.Hour}
}

type verificationStore struct {
	s        *Store
	cooldown time.Duration
}

func (v verificationStore) Current(ctx context.Context, userID string) (*trust.Record, error) {
	return v.s.FindUserTrust(ctx, userID)
}

func (v verificationStore) Latest(ctx context.Context, userID string) (*trust.Attempt, error) {
	attempt := trust.Attempt{UserID: userID}
	err := v.s.db.QueryRowContext(ctx, `
		SELECT checked_at, github_id, account_age_days, public_events, public_repos, oldest_repo_age_days, passed, reason
		FROM trust_verifications WHERE user_id = $1`, userID).Scan(&attempt.CheckedAt, &attempt.Evidence.GitHubID,
		&attempt.Evidence.AccountAgeDays, &attempt.Evidence.PublicEvents, &attempt.Evidence.PublicRepos,
		&attempt.Evidence.OldestRepoAgeDays, &attempt.Passed, &attempt.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find verification: %w", err)
	}
	return &attempt, nil
}

// Record writes the attempt and the decision it leads to in one transaction.
// A pass first claims "github:<id>"; a claim held by another account turns the
// attempt into a refusal, which is stored, and returns ErrGitHubClaimed.
func (v verificationStore) Record(ctx context.Context, attempt trust.Attempt) error {
	tx, err := v.s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("record verification: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockUserTrust(ctx, tx, attempt.UserID); err != nil {
		return err
	}
	current, err := storedTrustState(ctx, tx, attempt.UserID)
	if err != nil {
		return fmt.Errorf("record verification: read decision: %w", err)
	}
	if current == trust.Full {
		return trust.ErrAlreadyVerified
	}
	var claimErr error
	if attempt.Passed {
		claimErr = v.claim(ctx, tx, attempt.UserID, trust.ClaimKey(attempt.Evidence.GitHubID))
		if claimErr != nil && !errors.Is(claimErr, trust.ErrGitHubClaimed) {
			return claimErr
		}
		if claimErr != nil {
			attempt.Passed, attempt.Reason = false, trust.ReasonGitHubAlreadyUsed
		}
	}
	if err := writeVerification(ctx, tx, attempt); err != nil {
		return err
	}
	if err := writeUserTrust(ctx, tx, decisionOf(attempt), verificationActor); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("record verification: %w", err)
	}
	return claimErr
}

func decisionOf(attempt trust.Attempt) trust.Record {
	state := trust.Limited
	if attempt.Passed {
		state = trust.Full
	}
	return trust.Record{UserID: attempt.UserID, State: state, Method: trust.MethodGitHub, Reason: attempt.Reason, CheckVersion: trust.RuleVersion}
}

func writeVerification(ctx context.Context, tx *sql.Tx, attempt trust.Attempt) error {
	e := attempt.Evidence
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO trust_verifications (user_id, checked_at, github_id, account_age_days, public_events, public_repos, oldest_repo_age_days, passed, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (user_id) DO UPDATE SET checked_at = EXCLUDED.checked_at, github_id = EXCLUDED.github_id,
			account_age_days = EXCLUDED.account_age_days, public_events = EXCLUDED.public_events, public_repos = EXCLUDED.public_repos,
			oldest_repo_age_days = EXCLUDED.oldest_repo_age_days, passed = EXCLUDED.passed, reason = EXCLUDED.reason`,
		attempt.UserID, attempt.CheckedAt, e.GitHubID, e.AccountAgeDays, e.PublicEvents, e.PublicRepos, e.OldestRepoAgeDays,
		attempt.Passed, attempt.Reason); err != nil {
		return fmt.Errorf("record verification: %w", err)
	}
	return nil
}

// claim takes the key for the account, or reports ErrGitHubClaimed. The same
// account taking it again is a no-op; a released claim is open again only
// after the cooldown. Any other live claim of the account is released first,
// so an account never holds two.
func (v verificationStore) claim(ctx context.Context, tx *sql.Tx, userID, key string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE trust_claims SET released_at = now() WHERE user_id = $1 AND released_at IS NULL AND claim_key <> $2`, userID, key); err != nil {
		return fmt.Errorf("claim: release the old claim: %w", err)
	}
	inserted, err := tx.ExecContext(ctx, `INSERT INTO trust_claims (claim_key, user_id) VALUES ($1, $2) ON CONFLICT (claim_key) DO NOTHING`, key, userID)
	if err != nil {
		return fmt.Errorf("claim: %w", err)
	}
	if n, _ := inserted.RowsAffected(); n == 1 {
		return nil
	}
	var holder sql.NullString
	var released sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT user_id, released_at FROM trust_claims WHERE claim_key = $1 FOR UPDATE`, key).Scan(&holder, &released); err != nil {
		return fmt.Errorf("claim: read: %w", err)
	}
	switch {
	case !released.Valid && holder.String == userID:
		return nil
	case !released.Valid, time.Since(released.Time) < v.cooldown:
		return trust.ErrGitHubClaimed
	}
	if _, err := tx.ExecContext(ctx, `UPDATE trust_claims SET user_id = $2, claimed_at = now(), released_at = NULL WHERE claim_key = $1`, key, userID); err != nil {
		return fmt.Errorf("claim: take over: %w", err)
	}
	return nil
}
