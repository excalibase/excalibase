package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
)

const (
	overviewLimit  = 20
	topProjects    = 5
	signupDomainCt = 10
)

// projectBriefSQL selects a project with its owner and latest usage; the
// trailing clause adds the filter and order each card needs.
const projectBriefSQL = `
	WITH usage AS (` + latestMetricsSQL + `)
	SELECT di.project_id, COALESCE(di.project_name, ''), di.org_id, COALESCE(u.email, ''), ` + statusExpr + `,
	       di.status, COALESCE(di.failure_reason, ''), COALESCE(usage.cpu_usage_cores, 0), COALESCE(usage.memory_usage_mb, 0)
	FROM database_instances di
	JOIN orgs o ON o.id = di.org_id
	JOIN users u ON u.id = o.owner_id
	LEFT JOIN abuse_suspensions s ON s.user_id = u.id
	LEFT JOIN usage ON usage.project_id = di.project_id `

func (a *AdminConsole) Overview(ctx context.Context, since time.Time) (*adminconsole.Overview, error) {
	overview := &adminconsole.Overview{
		NewlySuspended: []adminconsole.SuspendedBrief{}, SignupDomains: []adminconsole.DomainCount{},
		FailedProjects: []adminconsole.ProjectBrief{}, TopCPU: []adminconsole.ProjectBrief{},
		TopMemory: []adminconsole.ProjectBrief{}, TopEgress: []adminconsole.ProjectBrief{},
	}
	var err error
	if overview.NewlySuspended, err = a.newlySuspended(ctx, since); err != nil {
		return nil, err
	}
	if overview.Signups24h, overview.SignupDomains, err = a.signups(ctx, since); err != nil {
		return nil, err
	}
	if overview.FailedProjects, err = a.projectBriefs(ctx,
		`WHERE di.status = 'FAILED' OR di.current_stage = 'FAILED' ORDER BY di.updated_at DESC NULLS LAST LIMIT $1`, overviewLimit); err != nil {
		return nil, fmt.Errorf("failed projects: %w", err)
	}
	if overview.TopCPU, err = a.projectBriefs(ctx, `WHERE usage.cpu_usage_cores > 0 ORDER BY usage.cpu_usage_cores DESC LIMIT $1`, topProjects); err != nil {
		return nil, fmt.Errorf("top cpu: %w", err)
	}
	if overview.TopMemory, err = a.projectBriefs(ctx, `WHERE usage.memory_usage_mb > 0 ORDER BY usage.memory_usage_mb DESC LIMIT $1`, topProjects); err != nil {
		return nil, fmt.Errorf("top memory: %w", err)
	}
	if overview.TopEgress, err = a.topEgress(ctx); err != nil {
		return nil, err
	}
	return overview, nil
}

func (a *AdminConsole) projectBriefs(ctx context.Context, clause string, limit int) ([]adminconsole.ProjectBrief, error) {
	rows, err := a.db.QueryContext(ctx, projectBriefSQL+clause, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []adminconsole.ProjectBrief{}
	for rows.Next() {
		var brief adminconsole.ProjectBrief
		if err := rows.Scan(&brief.ProjectID, &brief.ProjectName, &brief.OrgID, &brief.OwnerEmail, &brief.OwnerStatus,
			&brief.Status, &brief.FailureReason, &brief.CPUCores, &brief.MemoryMB); err != nil {
			return nil, err
		}
		out = append(out, brief)
	}
	return out, rows.Err()
}

func (a *AdminConsole) newlySuspended(ctx context.Context, since time.Time) ([]adminconsole.SuspendedBrief, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT user_id, COALESCE(email, ''), COALESCE(reason, ''), suspended_at
		FROM abuse_suspensions WHERE suspended_at > $1 ORDER BY suspended_at DESC LIMIT $2`, since, overviewLimit)
	if err != nil {
		return nil, fmt.Errorf("newly suspended: %w", err)
	}
	defer rows.Close()
	out := []adminconsole.SuspendedBrief{}
	for rows.Next() {
		var brief adminconsole.SuspendedBrief
		var at sql.NullTime
		if err := rows.Scan(&brief.UserID, &brief.Email, &brief.Reason, &at); err != nil {
			return nil, err
		}
		brief.SuspendedAt = at.Time
		out = append(out, brief)
	}
	return out, rows.Err()
}

func (a *AdminConsole) signups(ctx context.Context, since time.Time) (int, []adminconsole.DomainCount, error) {
	var total int
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE kind = 'human' AND created_at > $1`, since).Scan(&total); err != nil {
		return 0, nil, fmt.Errorf("count signups: %w", err)
	}
	rows, err := a.db.QueryContext(ctx, `
		SELECT lower(split_part(email, '@', 2)) AS domain, COUNT(*)
		FROM users WHERE kind = 'human' AND created_at > $1
		GROUP BY domain ORDER BY COUNT(*) DESC, domain LIMIT $2`, since, signupDomainCt)
	if err != nil {
		return 0, nil, fmt.Errorf("signup domains: %w", err)
	}
	defer rows.Close()
	domains := []adminconsole.DomainCount{}
	for rows.Next() {
		var item adminconsole.DomainCount
		if err := rows.Scan(&item.Domain, &item.Count); err != nil {
			return 0, nil, err
		}
		domains = append(domains, item)
	}
	return total, domains, rows.Err()
}

// topEgress ranks projects by the transmit rate of their containers' newest
// sample. Only a single host records it; on a cluster the table is empty.
func (a *AdminConsole) topEgress(ctx context.Context) ([]adminconsole.ProjectBrief, error) {
	rows, err := a.db.QueryContext(ctx, `
		WITH newest AS (
			SELECT DISTINCT ON (container_name) container_name, project_id, network_tx_bytes_per_sec
			FROM container_metrics
			WHERE timestamp > NOW() - INTERVAL '`+metricsWindow+`' AND project_id <> ''
			ORDER BY container_name, timestamp DESC
		), egress AS (
			SELECT project_id, SUM(network_tx_bytes_per_sec) AS tx FROM newest GROUP BY project_id
		)
		SELECT di.project_id, COALESCE(di.project_name, ''), di.org_id, COALESCE(u.email, ''), `+statusExpr+`,
		       di.status, egress.tx
		FROM egress
		JOIN database_instances di ON di.project_id = egress.project_id
		JOIN orgs o ON o.id = di.org_id
		JOIN users u ON u.id = o.owner_id
		LEFT JOIN abuse_suspensions s ON s.user_id = u.id
		WHERE egress.tx > 0
		ORDER BY egress.tx DESC LIMIT $1`, topProjects)
	if err != nil {
		return nil, fmt.Errorf("top egress: %w", err)
	}
	defer rows.Close()
	out := []adminconsole.ProjectBrief{}
	for rows.Next() {
		var brief adminconsole.ProjectBrief
		if err := rows.Scan(&brief.ProjectID, &brief.ProjectName, &brief.OrgID, &brief.OwnerEmail, &brief.OwnerStatus, &brief.Status, &brief.EgressBytesPS); err != nil {
			return nil, err
		}
		out = append(out, brief)
	}
	return out, rows.Err()
}
