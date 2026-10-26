package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// signInMailLockSpace serialises one project's budget check and insert.
const signInMailLockSpace = 662

// SignInMailProject reads the project's name, site and email sign-in switch;
// an unknown project reads as switched off.
func (s *Store) SignInMailProject(ctx context.Context, projectID string) (domain.SignInMailProject, error) {
	var project domain.SignInMailProject
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(i.project_name, ''), COALESCE(a.site_url, ''), COALESCE(a.email_otp_enabled, false)
		 FROM database_instances i LEFT JOIN project_auth_settings a ON a.project_id = i.project_id
		 WHERE i.project_id = $1`, projectID).
		Scan(&project.Name, &project.SiteURL, &project.EmailOTPEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SignInMailProject{}, nil
	}
	if err != nil {
		return domain.SignInMailProject{}, fmt.Errorf("read sign-in mail project: %w", err)
	}
	return project, nil
}

// TakeSignInMail records one sign-in mail when the project has sent fewer than
// perDay in the last day, and drops every row older than that.
func (s *Store) TakeSignInMail(ctx context.Context, projectID string, perDay int) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin sign-in mail: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, signInMailLockSpace, projectID); err != nil {
		return false, fmt.Errorf("lock sign-in mail budget: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM sign_in_mail_sends WHERE sent_at < NOW() - INTERVAL '1 day'`); err != nil {
		return false, fmt.Errorf("prune sign-in mail: %w", err)
	}
	var sent int
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM sign_in_mail_sends WHERE project_id = $1`, projectID).Scan(&sent); err != nil {
		return false, fmt.Errorf("count sign-in mail: %w", err)
	}
	if sent >= perDay {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sign_in_mail_sends (project_id) VALUES ($1)`, projectID); err != nil {
		return false, fmt.Errorf("record sign-in mail: %w", err)
	}
	return true, tx.Commit()
}
