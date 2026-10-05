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
		`INSERT INTO studio_oauth_states (state_hash, provider, code_verifier, invite_hash, expires_at) VALUES ($1, $2, $3, $4, $5)`,
		stateHash, state.Provider, state.CodeVerifier, state.InviteHash, expiresAt.UTC())
	return err
}

func (s *Store) ConsumeOAuthState(ctx context.Context, stateHash string, now time.Time) (*domain.OAuthState, error) {
	var state domain.OAuthState
	err := s.db.QueryRowContext(ctx,
		`DELETE FROM studio_oauth_states WHERE state_hash = $1 AND expires_at > $2
		 RETURNING provider, code_verifier, invite_hash`, stateHash, now.UTC()).
		Scan(&state.Provider, &state.CodeVerifier, &state.InviteHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrOAuthStateInvalid
	}
	if err != nil {
		return nil, err
	}
	return &state, nil
}

func (s *Store) FindUserByIdentity(ctx context.Context, provider, subject string) (*domain.User, error) {
	return scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+prefixedUserColumns+` FROM users u JOIN studio_identities i ON i.user_id = u.id
		 WHERE i.provider = $1 AND i.subject = $2`, provider, subject))
}

func (s *Store) LinkStudioIdentity(ctx context.Context, provider, subject, userID, email string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO studio_identities (provider, subject, user_id, email) VALUES ($1, $2, $3, $4)`,
		provider, subject, userID, email)
	return err
}
