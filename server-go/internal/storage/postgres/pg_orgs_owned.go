package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/lib/pq"
)

// orgCreatorLockSpace namespaces the per-account advisory lock org creation
// runs under, apart from the per-org project-limit locks (421).
const orgCreatorLockSpace = 553

// orgSlugConstraint is the unique constraint on orgs.slug.
const orgSlugConstraint = "orgs_slug_key"

// CreateOrgWithOwner: see storage.OrgStore. The creator's lock is held to
// commit, so two concurrent creates by one account count each other's org.
func (s *Store) CreateOrgWithOwner(ctx context.Context, org *domain.Org, maxFreeOrgs int) error {
	return s.underCreatorLock(ctx, org.OwnerID, func(tx *sql.Tx) error {
		if maxFreeOrgs > 0 && org.Tier == domain.Free {
			var held int
			if err := tx.QueryRowContext(ctx,
				`SELECT count(*) FROM orgs WHERE owner_id = $1 AND upper(tier) = 'FREE'`,
				org.OwnerID).Scan(&held); err != nil {
				return fmt.Errorf("count free orgs: %w", err)
			}
			if held >= maxFreeOrgs {
				return storage.ErrFreeOrgLimitReached
			}
		}
		return insertOwnedOrg(ctx, tx, org)
	})
}

// EnsurePersonalOrg: see storage.OrgStore. It takes the lock
// CreateOrgWithOwner takes, so it and a manual create never both land a
// free org.
func (s *Store) EnsurePersonalOrg(ctx context.Context, org *domain.Org) (bool, error) {
	created := false
	err := s.underCreatorLock(ctx, org.OwnerID, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM orgs WHERE owner_id = $1)`, org.OwnerID).Scan(&exists); err != nil {
			return fmt.Errorf("find orgs created by the account: %w", err)
		}
		if exists {
			return nil
		}
		if err := insertOwnedOrg(ctx, tx, org); err != nil {
			return err
		}
		created = true
		return nil
	})
	return created && err == nil, err
}

func (s *Store) underCreatorLock(ctx context.Context, ownerID string, write func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin org create: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`,
		orgCreatorLockSpace, ownerID); err != nil {
		return fmt.Errorf("lock org creator: %w", err)
	}
	if err := write(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func insertOwnedOrg(ctx context.Context, tx *sql.Tx, org *domain.Org) error {
	now := time.Now().UTC()
	_, err := tx.ExecContext(ctx,
		`INSERT INTO orgs (id, name, slug, tier, owner_id, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		org.ID, org.Name, org.Slug, org.Tier, org.OwnerID, now, now)
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && string(pqErr.Code) == uniqueViolation && pqErr.Constraint == orgSlugConstraint {
		return storage.ErrOrgSlugTaken
	}
	if err != nil {
		return fmt.Errorf("insert org: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO org_members (org_id, user_id, role, created_at) VALUES ($1, $2, $3, $4)`,
		org.ID, org.OwnerID, domain.OrgRoleOwner, now); err != nil {
		return fmt.Errorf("add org owner: %w", err)
	}
	return nil
}
