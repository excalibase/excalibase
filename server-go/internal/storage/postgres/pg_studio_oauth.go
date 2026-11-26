package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func (s *Store) SaveOAuthState(ctx context.Context, stateHash string, state domain.OAuthState, expiresAt time.Time) error {
	// Expired rows are swept on write so the table stays the size of the
	// sign-ins in flight.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM studio_oauth_states WHERE expires_at < $1`, time.Now().UTC()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO studio_oauth_states (state_hash, provider, code_verifier, invite_hash, purpose, user_id, expires_at) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		stateHash, state.Provider, state.CodeVerifier, state.InviteHash, state.Purpose, state.UserID, expiresAt.UTC())
	return err
}

func (s *Store) ConsumeOAuthState(ctx context.Context, stateHash string, now time.Time) (*domain.OAuthState, error) {
	var state domain.OAuthState
	err := s.db.QueryRowContext(ctx,
		`DELETE FROM studio_oauth_states WHERE state_hash = $1 AND expires_at > $2
		 RETURNING provider, code_verifier, invite_hash, purpose, user_id`, stateHash, now.UTC()).
		Scan(&state.Provider, &state.CodeVerifier, &state.InviteHash, &state.Purpose, &state.UserID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrOAuthStateInvalid
	}
	if err != nil {
		return nil, err
	}
	return &state, nil
}

const findUserByIdentitySQL = `SELECT u.id, u.username, u.email, u.password_hash, u.role, u.active, u.kind, u.email_verified_at, u.created_at, u.updated_at FROM users u JOIN studio_identities i ON i.user_id = u.id
		 WHERE i.provider = $1 AND i.subject = $2`

func (s *Store) FindUserByIdentity(ctx context.Context, provider, subject string) (*domain.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, findUserByIdentitySQL, provider, subject))
}

func (s *Store) LinkStudioIdentity(ctx context.Context, provider, subject, userID, email string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// The lock a grant holds while it reads the person's identities: a link either lands before the grant
	// reads them, and is claimed by it, or after the grant, and claims itself below.
	// It is taken even with credits off: a link is rare and brief, and a flag check here would race the flag.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, 0)`, creditGrantLockSpace); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO studio_identities (provider, subject, user_id, email) VALUES ($1, $2, $3, $4)`,
		provider, subject, userID, email); err != nil {
		return err
	}
	// A person who already holds the sign-up credit claims the new identity
	// too, so it can never earn a second grant on another account. A subject
	// someone else claimed first is left alone and does not block the link.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO credit_grant_claims (claim_kind, claim_value, user_id, org_id, entry_id, terms_version, terms_accepted_at)
		 SELECT $1, $2, user_id, org_id, entry_id, terms_version, terms_accepted_at FROM credit_grant_claims
		 WHERE claim_kind = 'user' AND claim_value = $3 AND entry_id IS NOT NULL AND $1 IN ('github', 'google')
		 ON CONFLICT DO NOTHING`, provider, subject, userID); err != nil {
		return err
	}
	return tx.Commit()
}
