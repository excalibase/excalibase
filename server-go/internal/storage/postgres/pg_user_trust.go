package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/trust"
)

// FindUserTrust returns the account's decision, or nil when it has none.
func (s *Store) FindUserTrust(ctx context.Context, userID string) (*trust.Record, error) {
	var record trust.Record
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id, state, method, reason, decided_at, check_version FROM user_trust WHERE user_id = $1`,
		userID).Scan(&record.UserID, &record.State, &record.Method, &record.Reason, &record.DecidedAt, &record.CheckVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find trust: %w", err)
	}
	return &record, nil
}

// SaveUserTrust stores the decision and appends its audit entry in one
// transaction. The row is locked first, so concurrent saves chain: each
// audit entry starts from the state the one before it ended on.
func (s *Store) SaveUserTrust(ctx context.Context, record trust.Record, actor string) error {
	if err := record.Validate(); err != nil {
		return err
	}
	if actor == "" {
		return fmt.Errorf("%w: no actor", trust.ErrInvalidRecord)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save trust: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockUserTrust(ctx, tx, record.UserID); err != nil {
		return err
	}
	var previous sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT state FROM user_trust WHERE user_id = $1`, record.UserID).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("save trust: read previous: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO user_trust (user_id, state, method, reason, check_version, decided_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (user_id) DO UPDATE SET state = EXCLUDED.state, method = EXCLUDED.method,
			reason = EXCLUDED.reason, check_version = EXCLUDED.check_version, decided_at = now()`,
		record.UserID, record.State, record.Method, record.Reason, record.CheckVersion); err != nil {
		return fmt.Errorf("save trust: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO trust_audit (user_id, from_state, to_state, method, reason, actor)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		record.UserID, previous, record.State, record.Method, record.Reason, actor); err != nil {
		return fmt.Errorf("save trust: audit: %w", err)
	}
	return tx.Commit()
}

// trustLockSpace namespaces the per-account advisory lock a decision is saved under.
const trustLockSpace = 899

// lockUserTrust serialises decisions for one account, including the first one,
// when there is no row yet for SELECT ... FOR UPDATE to lock.
func lockUserTrust(ctx context.Context, tx *sql.Tx, userID string) error {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, trustLockSpace, userID); err != nil {
		return fmt.Errorf("save trust: lock: %w", err)
	}
	return nil
}

// Trust adapts the store to trust.Store.
func (s *Store) Trust() trust.Store { return trustStore{s} }

type trustStore struct{ s *Store }

func (t trustStore) Find(ctx context.Context, userID string) (*trust.Record, error) {
	return t.s.FindUserTrust(ctx, userID)
}

func (t trustStore) Save(ctx context.Context, record trust.Record, actor string) error {
	return t.s.SaveUserTrust(ctx, record, actor)
}

// LatestBackfill returns the latest finished grandfather backfill, or nil.
func (s *Store) LatestBackfill(ctx context.Context) (*trust.BackfillRun, error) {
	var run trust.BackfillRun
	err := s.db.QueryRowContext(ctx,
		`SELECT finished_at, cutoff FROM trust_backfill_runs ORDER BY id DESC LIMIT 1`).Scan(&run.FinishedAt, &run.Cutoff)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find trust backfill: %w", err)
	}
	return &run, nil
}

// RecordBackfill is written by the backfill when it finishes.
func (s *Store) RecordBackfill(ctx context.Context, run trust.BackfillRun) error {
	if run.FinishedAt.IsZero() || run.Cutoff.IsZero() {
		return fmt.Errorf("%w: a backfill marker needs both times", trust.ErrInvalidRecord)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO trust_backfill_runs (finished_at, cutoff) VALUES ($1, $2)`, run.FinishedAt, run.Cutoff); err != nil {
		return fmt.Errorf("record trust backfill: %w", err)
	}
	return nil
}

// backfillActor is the audit actor of the grandfather backfill.
const backfillActor = "backfill"

// GrandfatherUsersBefore gives every account created before cutoff that holds
// no decision a full one, with its audit entry, and says how many. An account
// with a decision (an admin's limit, say) is left alone, and one created on or
// after cutoff is not touched: it must verify.
func (s *Store) GrandfatherUsersBefore(ctx context.Context, cutoff time.Time) (int, error) {
	result, err := s.db.ExecContext(ctx, `
		WITH granted AS (
			INSERT INTO user_trust (user_id, state, method, reason, check_version)
			SELECT id, 'full', 'grandfathered', 'grandfathered', 1 FROM users WHERE created_at < $1
			ON CONFLICT (user_id) DO NOTHING
			RETURNING user_id)
		INSERT INTO trust_audit (user_id, from_state, to_state, method, reason, actor)
		SELECT user_id, NULL, 'full', 'grandfathered', 'grandfathered', $2 FROM granted`, cutoff, backfillActor)
	if err != nil {
		return 0, fmt.Errorf("grandfather users: %w", err)
	}
	granted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("grandfather users: %w", err)
	}
	return int(granted), nil
}
