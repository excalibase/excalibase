package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
)

func (a *AdminConsole) ProjectCore(ctx context.Context, projectID string) (*adminconsole.ProjectCore, error) {
	var core adminconsole.ProjectCore
	var createdAt sql.NullTime
	var postgresVersion, mysqlVersion string
	err := a.db.QueryRowContext(ctx, projectCoreSQL, projectID).Scan(
		&core.ProjectID, &core.ProjectName, &core.OrgID, &core.OrgName, &core.OrgSlug, &core.Tier, &core.Namespace,
		&core.OwnerID, &core.OwnerEmail, &core.OwnerStatus, &core.OwnerReason,
		&core.DBType, &postgresVersion, &mysqlVersion, &core.DBStatus, &core.NoDatabase,
		&core.FailureReason, &core.CPUCores, &core.MemoryMB, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read project: %w", err)
	}
	core.DBVersion = postgresVersion
	if core.DBVersion == "" {
		core.DBVersion = mysqlVersion
	}
	core.CreatedAt = timePtr(createdAt)
	return &core, nil
}

func (a *AdminConsole) ProjectSites(ctx context.Context, projectID string) ([]adminconsole.SiteBrief, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT id, name, hostname FROM sites WHERE project_id = $1 ORDER BY created_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []adminconsole.SiteBrief{}
	for rows.Next() {
		var site adminconsole.SiteBrief
		if err := rows.Scan(&site.ID, &site.Name, &site.Hostname); err != nil {
			return nil, err
		}
		out = append(out, site)
	}
	return out, rows.Err()
}

func (a *AdminConsole) ProjectFunctions(ctx context.Context, projectID string) ([]adminconsole.FunctionBrief, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT id, active FROM edge_functions WHERE project_id = $1 ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []adminconsole.FunctionBrief{}
	for rows.Next() {
		var fn adminconsole.FunctionBrief
		if err := rows.Scan(&fn.ID, &fn.Active); err != nil {
			return nil, err
		}
		out = append(out, fn)
	}
	return out, rows.Err()
}

func (a *AdminConsole) ProjectAudit(ctx context.Context, projectID string, limit int) ([]adminconsole.AuditBrief, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT action, COALESCE(user_id, ''), COALESCE(details, ''), timestamp
		FROM audit_log WHERE project_id = $1 ORDER BY timestamp DESC, id DESC LIMIT $2`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []adminconsole.AuditBrief{}
	for rows.Next() {
		var event adminconsole.AuditBrief
		var at sql.NullTime
		if err := rows.Scan(&event.Action, &event.UserID, &event.Details, &at); err != nil {
			return nil, err
		}
		event.Timestamp = timePtr(at)
		out = append(out, event)
	}
	return out, rows.Err()
}

// ProjectFacts names every project's org and owner and gives its latest stored
// usage, for the project list. One query for the whole list, never one per row.
func (a *AdminConsole) ProjectFacts(ctx context.Context) (map[string]adminconsole.ProjectFact, error) {
	rows, err := a.db.QueryContext(ctx, projectFactsSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	facts := map[string]adminconsole.ProjectFact{}
	for rows.Next() {
		var projectID string
		var fact adminconsole.ProjectFact
		if err := rows.Scan(&projectID, &fact.OrgName, &fact.OwnerEmail, &fact.OwnerStatus, &fact.HasUsage, &fact.CPUCores, &fact.MemoryMB); err != nil {
			return nil, err
		}
		facts[projectID] = fact
	}
	return facts, rows.Err()
}

// projectCoreSQL is composed at compile time from constant fragments.
const projectCoreSQL = `
		WITH usage AS (` + latestMetricsSQL + `)
		SELECT di.project_id, COALESCE(di.project_name, ''), di.org_id, o.name, o.slug, di.tier, COALESCE(di.namespace, ''),
		       o.owner_id, COALESCE(u.email, ''), ` + statusExpr + `, COALESCE(s.reason, ''),
		       di.database_type, COALESCE(di.postgres_version, ''), COALESCE(di.mysql_version, ''), di.status, di.no_database,
		       COALESCE(di.failure_reason, ''), COALESCE(usage.cpu_usage_cores, 0), COALESCE(usage.memory_usage_mb, 0), di.created_at
		FROM database_instances di
		JOIN orgs o ON o.id = di.org_id
		JOIN users u ON u.id = o.owner_id
		LEFT JOIN abuse_suspensions s ON s.user_id = u.id
		LEFT JOIN usage ON usage.project_id = di.project_id
		WHERE di.project_id = $1`

// projectFactsSQL is composed at compile time from constant fragments.
const projectFactsSQL = `
		WITH usage AS (` + latestMetricsSQL + `)
		SELECT di.project_id, o.name, COALESCE(u.email, ''), ` + statusExpr + `,
		       usage.project_id IS NOT NULL, COALESCE(usage.cpu_usage_cores, 0), COALESCE(usage.memory_usage_mb, 0)
		FROM database_instances di
		JOIN orgs o ON o.id = di.org_id
		JOIN users u ON u.id = o.owner_id
		LEFT JOIN abuse_suspensions s ON s.user_id = u.id
		LEFT JOIN usage ON usage.project_id = di.project_id`
