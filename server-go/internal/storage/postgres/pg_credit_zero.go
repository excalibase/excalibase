package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/credits"
	"github.com/excalibase/provisioning-poc/internal/creditzero"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/planbudget"
)

// CreditZeroStore is the platform database behind what happens at $0 (EXC-866 C.5).
// It satisfies creditzero.Store.
type CreditZeroStore struct {
	db *sql.DB
}

var _ creditzero.Store = (*CreditZeroStore)(nil)

func NewCreditZeroStore(store *Store) *CreditZeroStore { return &CreditZeroStore{db: store.db} }

// Now is the database clock: the grace period is measured on it.
func (z *CreditZeroStore) Now(ctx context.Context) (time.Time, error) {
	var now time.Time
	err := z.db.QueryRowContext(ctx, `SELECT now()`).Scan(&now)
	return now.UTC(), err
}

// Orgs lists every org that holds a credit account, a grace period or a service
// paused for credit. An org with none of them has nothing to enforce or release.
func (z *CreditZeroStore) Orgs(ctx context.Context) ([]creditzero.OrgState, error) {
	return z.readOrgs(ctx, "")
}

// Org reads one org again, for the check just before a pause. An org that holds no
// account, grace period or paused service comes back as a bare id: never at $0.
func (z *CreditZeroStore) Org(ctx context.Context, orgID string) (creditzero.OrgState, error) {
	orgs, err := z.readOrgs(ctx, orgID)
	if err != nil || len(orgs) == 0 {
		return creditzero.OrgState{OrgID: orgID}, err
	}
	return orgs[0], nil
}

func (z *CreditZeroStore) readOrgs(ctx context.Context, onlyOrg string) ([]creditzero.OrgState, error) {
	rows, err := z.db.QueryContext(ctx, `
		SELECT o.id, o.tier,
		       (a.org_id IS NOT NULL AND a.balance_micros <= 0 AND upper(o.tier) = $1),
		       z.since, (s.user_id IS NOT NULL)
		FROM orgs o
		LEFT JOIN credit_accounts a ON a.org_id = o.id
		LEFT JOIN credit_zero_orgs z ON z.org_id = o.id
		LEFT JOIN abuse_suspensions s ON s.user_id = o.owner_id
		WHERE ($2 = '' OR o.id = $2)
		  AND (a.org_id IS NOT NULL OR z.org_id IS NOT NULL
		       OR EXISTS (SELECT 1 FROM credit_paused_services p WHERE p.org_id = o.id))
		ORDER BY o.id`, string(domain.Free), onlyOrg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var orgs []creditzero.OrgState
	for rows.Next() {
		var org creditzero.OrgState
		var since sql.NullTime
		if err := rows.Scan(&org.OrgID, &org.Tier, &org.AtZero, &since, &org.OwnerSuspended); err != nil {
			return nil, err
		}
		if since.Valid {
			at := since.Time.UTC()
			org.Since = &at
		}
		orgs = append(orgs, org)
	}
	return orgs, rows.Err()
}

// MarkZero starts the org's grace period and keeps the date of one already running.
func (z *CreditZeroStore) MarkZero(ctx context.Context, orgID string) error {
	_, err := z.db.ExecContext(ctx, `INSERT INTO credit_zero_orgs (org_id) VALUES ($1) ON CONFLICT (org_id) DO NOTHING`, orgID)
	return err
}

func (z *CreditZeroStore) ClearZero(ctx context.Context, orgID string) error {
	_, err := z.db.ExecContext(ctx, `DELETE FROM credit_zero_orgs WHERE org_id = $1`, orgID)
	return err
}

// Holdings is what the org reserves in CPU and memory, summed exactly as the plan
// budget sums it, and the running databases and apps that could be paused.
func (z *CreditZeroStore) Holdings(ctx context.Context, orgID string) (creditzero.Holdings, error) {
	used, err := planbudget.OrgUsage(ctx, z.db, orgID)
	if err != nil {
		return creditzero.Holdings{}, err
	}
	held := creditzero.Holdings{CPUMilli: used.CPUMilli, MemoryBytes: used.MemoryBytes}
	if held.Transitioning, err = z.inTransition(ctx, orgID); err != nil {
		return held, err
	}
	if held.Pausable, err = z.runningDatabases(ctx, orgID); err != nil {
		return held, err
	}
	apps, err := z.runningApps(ctx, orgID)
	held.Pausable = append(held.Pausable, apps...)
	return held, err
}

// inTransition says whether any database or app of the org is starting, stopping, resuming or being restored.
func (z *CreditZeroStore) inTransition(ctx context.Context, orgID string) (bool, error) {
	var moving bool
	err := z.db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM database_instances WHERE org_id = $1 AND status = ANY($2))
		    OR EXISTS (SELECT 1 FROM apps a JOIN database_instances d ON d.project_id = a.project_id
		               WHERE d.org_id = $1 AND a.status = ANY($3))`,
		orgID, pq.Array(transitionalDatabaseStatuses), pq.Array(transitionalAppStatuses)).Scan(&moving)
	return moving, err
}

var (
	transitionalDatabaseStatuses = []string{string(domain.StatusPausing), string(domain.StatusResuming), domain.StatusProvisioning, string(domain.StatusRestoring)}
	transitionalAppStatuses      = []string{apphost.StatusPausing, apphost.StatusResuming, apphost.StatusCreated}
)

func (z *CreditZeroStore) runningDatabases(ctx context.Context, orgID string) ([]creditzero.Service, error) {
	rows, err := z.db.QueryContext(ctx, `
		SELECT project_id, project_name, reserved_cpu_milli, reserved_memory_bytes, reserved_disk_bytes, reserved_copies, COALESCE(created_at, 'epoch'::timestamptz)
		FROM database_instances
		WHERE org_id = $1 AND status = $2 AND NOT no_database AND reserved_copies > 0`, orgID, string(domain.StatusActive))
	if err != nil {
		return nil, fmt.Errorf("read running databases: %w", err)
	}
	defer rows.Close()
	var services []creditzero.Service
	for rows.Next() {
		var service creditzero.Service
		var db planbudget.Database
		if err := rows.Scan(&service.ProjectID, &service.Name, &db.PerCopy.CPUMilli, &db.PerCopy.MemoryBytes, &db.PerCopy.DiskBytes, &db.Copies, &service.CreatedAt); err != nil {
			return nil, err
		}
		db.Status = string(domain.StatusActive)
		size := planbudget.ReservedDatabase(db)
		service.Kind, service.ServiceID = creditzero.KindDatabase, service.ProjectID
		service.CPUMilli, service.MemoryBytes = size.CPUMilli, size.MemoryBytes
		services = append(services, service)
	}
	return services, rows.Err()
}

func (z *CreditZeroStore) runningApps(ctx context.Context, orgID string) ([]creditzero.Service, error) {
	rows, err := z.db.QueryContext(ctx, `
		SELECT a.project_id, a.id, a.name, a.doc, a.created_at
		FROM apps a JOIN database_instances d ON d.project_id = a.project_id
		WHERE d.org_id = $1 AND a.status = $2 AND COALESCE((a.doc->>'replicas')::int, 0) > 0`, orgID, apphost.StatusRunning)
	if err != nil {
		return nil, fmt.Errorf("read running apps: %w", err)
	}
	defer rows.Close()
	var services []creditzero.Service
	for rows.Next() {
		var service creditzero.Service
		var doc []byte
		if err := rows.Scan(&service.ProjectID, &service.ServiceID, &service.Name, &doc, &service.CreatedAt); err != nil {
			return nil, err
		}
		size, err := planbudget.ReservedApp(doc)
		if err != nil {
			return nil, fmt.Errorf("size of app %s: %w", service.ServiceID, err)
		}
		service.Kind, service.CPUMilli, service.MemoryBytes = creditzero.KindApp, size.CPUMilli, size.MemoryBytes
		services = append(services, service)
	}
	return services, rows.Err()
}

// RecordPause writes down a service the credit is about to pause; recording it again refreshes the row.
func (z *CreditZeroStore) RecordPause(ctx context.Context, orgID string, row creditzero.Paused) error {
	_, err := z.db.ExecContext(ctx, `
		INSERT INTO credit_paused_services (org_id, kind, project_id, service_id, service_name)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (org_id, kind, project_id, service_id)
		DO UPDATE SET service_name = EXCLUDED.service_name, paused_at = now()`,
		orgID, row.Kind, row.ProjectID, row.ServiceID, row.Name)
	return err
}

func (z *CreditZeroStore) ForgetPause(ctx context.Context, orgID string, row creditzero.Paused) error {
	_, err := z.db.ExecContext(ctx,
		`DELETE FROM credit_paused_services WHERE org_id = $1 AND kind = $2 AND project_id = $3 AND service_id = $4`,
		orgID, row.Kind, row.ProjectID, row.ServiceID)
	return err
}

// Paused lists the org's recorded services, each with what became of it, read from the live service.
func (z *CreditZeroStore) Paused(ctx context.Context, orgID string) ([]creditzero.Paused, error) {
	rows, err := z.db.QueryContext(ctx, `
		SELECT p.kind, p.project_id, p.service_id, p.service_name, p.paused_at,
		       d.status, COALESCE(d.pause_reason, ''), a.status
		FROM credit_paused_services p
		LEFT JOIN database_instances d ON p.kind = 'database' AND d.project_id = p.project_id
		LEFT JOIN apps a ON p.kind = 'app' AND a.project_id = p.project_id AND a.id = p.service_id
		WHERE p.org_id = $1 ORDER BY p.paused_at, p.service_id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paused []creditzero.Paused
	for rows.Next() {
		var row creditzero.Paused
		var databaseStatus, appStatus sql.NullString
		var reason string
		if err := rows.Scan(&row.Kind, &row.ProjectID, &row.ServiceID, &row.Name, &row.PausedAt, &databaseStatus, &reason, &appStatus); err != nil {
			return nil, err
		}
		row.PausedAt = row.PausedAt.UTC()
		row.State = holdOf(row.Kind, databaseStatus, reason, appStatus)
		paused = append(paused, row)
	}
	return paused, rows.Err()
}

// holdOf says whether a service is still paused for credit, from its live status.
// Only a settled pause is credit's to resume; a service changing state is looked at
// again on the next pass with its row kept, because resuming it now would do nothing
// and forgetting the row would leave it paused for good.
func holdOf(kind string, databaseStatus sql.NullString, databaseReason string, appStatus sql.NullString) creditzero.Hold {
	status := appStatus
	if kind == creditzero.KindDatabase {
		status = databaseStatus
	}
	if !status.Valid || domain.IsDeletionStatus(status.String) || status.String == string(domain.StatusPendingDeletion) {
		return creditzero.HoldGone
	}
	creditsOwn := kind == creditzero.KindApp || databaseReason == domain.PauseReasonCredits
	switch status.String {
	case string(domain.StatusPaused):
		if creditsOwn {
			return creditzero.HoldPaused
		}
		return creditzero.HoldReleased
	case string(domain.StatusActive):
		return creditzero.HoldReleased
	case string(domain.StatusPausing):
		if creditsOwn {
			return creditzero.HoldBusy
		}
		return creditzero.HoldReleased
	}
	return creditzero.HoldBusy
}

// PurgeOrphans drops what outlived its org: the ledger keeps accounts for deleted
// orgs, and nothing else may keep their rows.
func (z *CreditZeroStore) PurgeOrphans(ctx context.Context) error {
	_, err := z.db.ExecContext(ctx, `DELETE FROM credit_paused_services p WHERE NOT EXISTS (SELECT 1 FROM orgs o WHERE o.id = p.org_id)`)
	if err != nil {
		return err
	}
	_, err = z.db.ExecContext(ctx, `DELETE FROM credit_zero_orgs z WHERE NOT EXISTS (SELECT 1 FROM orgs o WHERE o.id = z.org_id)`)
	return err
}

// HeldForCredit says whether an app is recorded as paused for credit while its
// org is at $0: an unsuspend must leave such an app stopped.
func (z *CreditZeroStore) HeldForCredit(ctx context.Context, projectID, appID string) (bool, error) {
	var held bool
	err := z.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM credit_paused_services p
			JOIN credit_accounts a ON a.org_id = p.org_id
			JOIN orgs o ON o.id = p.org_id
			WHERE p.kind = 'app' AND p.project_id = $1 AND p.service_id = $2
			  AND a.balance_micros <= 0 AND upper(o.tier) = $3)`, projectID, appID, string(domain.Free)).Scan(&held)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return held, err
}

// OrgZero is where the org stands when its credit runs out, as Studio shows it:
// whether it is at $0, since when, and the services still paused for credit.
func (z *CreditZeroStore) OrgZero(ctx context.Context, orgID string) (credits.ZeroInfo, error) {
	var info credits.ZeroInfo
	atZero, err := planbudget.CreditUsedUp(ctx, z.db, orgID)
	if err != nil {
		return info, err
	}
	info.AtZero = atZero
	var since sql.NullTime
	err = z.db.QueryRowContext(ctx, `SELECT since FROM credit_zero_orgs WHERE org_id = $1`, orgID).Scan(&since)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return info, err
	}
	if since.Valid {
		at := since.Time.UTC()
		info.Since = &at
	}
	rows, err := z.Paused(ctx, orgID)
	if err != nil {
		return info, err
	}
	for _, row := range rows {
		if row.State == creditzero.HoldPaused || row.State == creditzero.HoldBusy {
			info.Paused = append(info.Paused, credits.PausedService{Kind: row.Kind, ProjectID: row.ProjectID, ServiceID: row.ServiceID, Name: row.Name, PausedAt: row.PausedAt})
		}
	}
	return info, nil
}
