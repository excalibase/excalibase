package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/apphost"
)

// AdminConsole reads and writes what the platform admin's console shows. It
// reads across every tenant, so it is mounted only behind platform_admin.
type AdminConsole struct{ db *sql.DB }

// NewAdminConsole serves the console from the platform database's connection.
func NewAdminConsole(db *sql.DB) *AdminConsole { return &AdminConsole{db: db} }

// AdminConsole is the console's queries on this store's connection.
func (s *Store) AdminConsole() *AdminConsole { return NewAdminConsole(s.db) }

var (
	_ adminconsole.Reader          = (*AdminConsole)(nil)
	_ adminconsole.SuspensionStore = (*AdminConsole)(nil)
)

// metricsWindow bounds "the latest sample" to a live one, so a project whose
// metrics stopped is not ranked by an old reading.
const metricsWindow = "15 minutes"

const latestMetricsSQL = `
	SELECT DISTINCT ON (project_id) project_id, cpu_usage_cores, memory_usage_mb
	FROM database_metrics
	WHERE timestamp > NOW() - INTERVAL '` + metricsWindow + `'
	ORDER BY project_id, timestamp DESC`

// statusExpr turns the owner's rows into the status the console shows.
const statusExpr = `CASE WHEN s.user_id IS NOT NULL THEN 'suspended' WHEN NOT u.active THEN 'disabled' ELSE 'active' END`

// escapeLike makes user input match literally inside a LIKE pattern.
func escapeLike(input string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(strings.TrimSpace(input))
}

func (a *AdminConsole) ListUsers(ctx context.Context, q adminconsole.UserQuery) (adminconsole.Page[adminconsole.UserRow], error) {
	page := adminconsole.Page[adminconsole.UserRow]{Items: []adminconsole.UserRow{}}
	rows, err := a.db.QueryContext(ctx, `
		WITH owned AS (
			SELECT o.owner_id AS user_id, COUNT(DISTINCT o.id) AS orgs, COUNT(DISTINCT di.project_id) AS projects
			FROM orgs o LEFT JOIN database_instances di ON di.org_id = o.id
			GROUP BY o.owner_id
		), running AS (
			SELECT o.owner_id AS user_id, COUNT(*) AS apps
			FROM apps ap
			JOIN database_instances di ON di.project_id = ap.project_id
			JOIN orgs o ON o.id = di.org_id
			WHERE ap.status = $1
			GROUP BY o.owner_id
		)
		SELECT u.id, u.email, u.username, u.role, `+statusExpr+`,
		       u.email_verified_at IS NOT NULL, u.created_at, u.last_login_at, u.last_login_ip,
		       COALESCE(s.reason, ''), s.suspended_at,
		       COALESCE(owned.orgs, 0), COALESCE(owned.projects, 0), COALESCE(running.apps, 0),
		       COUNT(*) OVER()
		FROM users u
		LEFT JOIN abuse_suspensions s ON s.user_id = u.id
		LEFT JOIN owned ON owned.user_id = u.id
		LEFT JOIN running ON running.user_id = u.id
		WHERE u.kind = 'human'
		  AND ($2 OR s.user_id IS NULL)
		  AND ($3 = '' OR lower(u.email) LIKE '%' || lower($3) || '%' ESCAPE '\'
		       OR u.last_login_ip = $4 OR u.last_login_ip LIKE $3 || '%' ESCAPE '\')
		ORDER BY u.created_at DESC NULLS LAST, u.id
		LIMIT $5 OFFSET $6`,
		apphost.StatusRunning, q.IncludeSuspended, escapeLike(q.Search), strings.TrimSpace(q.Search), q.Limit, q.Offset)
	if err != nil {
		return page, fmt.Errorf("query users: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row adminconsole.UserRow
		var signedUp, lastLogin, suspendedAt sql.NullTime
		var lastIP sql.NullString
		if err := rows.Scan(&row.ID, &row.Email, &row.Username, &row.Role, &row.Status, &row.EmailVerified,
			&signedUp, &lastLogin, &lastIP, &row.SuspendedReason, &suspendedAt,
			&row.OrgCount, &row.ProjectCount, &row.RunningApps, &page.Total); err != nil {
			return page, fmt.Errorf("scan user: %w", err)
		}
		row.SignedUpAt, row.LastLoginAt, row.SuspendedAt = timePtr(signedUp), timePtr(lastLogin), timePtr(suspendedAt)
		row.LastLoginIP = lastIP.String
		page.Items = append(page.Items, row)
	}
	return page, rows.Err()
}

func (a *AdminConsole) ListOrgs(ctx context.Context, q adminconsole.OrgQuery) (adminconsole.Page[adminconsole.OrgRow], error) {
	page := adminconsole.Page[adminconsole.OrgRow]{Items: []adminconsole.OrgRow{}}
	rows, err := a.db.QueryContext(ctx, `
		WITH usage AS (`+latestMetricsSQL+`),
		per_project AS (
			SELECT di.org_id, COUNT(*) AS projects,
			       COALESCE(SUM(usage.cpu_usage_cores), 0) AS cpu, COALESCE(SUM(usage.memory_usage_mb), 0) AS mem
			FROM database_instances di LEFT JOIN usage ON usage.project_id = di.project_id
			GROUP BY di.org_id
		), per_app AS (
			SELECT di.org_id, COUNT(*) AS running
			FROM apps ap JOIN database_instances di ON di.project_id = ap.project_id
			WHERE ap.status = $1 GROUP BY di.org_id
		)
		SELECT o.id, o.name, o.slug, o.tier, o.owner_id, COALESCE(u.email, ''), `+statusExpr+`,
		       COALESCE(per_project.projects, 0), COALESCE(per_app.running, 0),
		       COALESCE(per_project.cpu, 0), COALESCE(per_project.mem, 0),
		       o.created_at, COUNT(*) OVER()
		FROM orgs o
		JOIN users u ON u.id = o.owner_id
		LEFT JOIN abuse_suspensions s ON s.user_id = u.id
		LEFT JOIN per_project ON per_project.org_id = o.id
		LEFT JOIN per_app ON per_app.org_id = o.id
		WHERE ($2 OR s.user_id IS NULL)
		  AND ($3 = '' OR lower(o.name) LIKE '%' || lower($3) || '%' ESCAPE '\'
		       OR lower(o.slug) LIKE '%' || lower($3) || '%' ESCAPE '\'
		       OR lower(u.email) LIKE '%' || lower($3) || '%' ESCAPE '\')
		ORDER BY o.created_at DESC NULLS LAST, o.id
		LIMIT $4 OFFSET $5`,
		apphost.StatusRunning, q.IncludeSuspended, escapeLike(q.Search), q.Limit, q.Offset)
	if err != nil {
		return page, fmt.Errorf("query orgs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row adminconsole.OrgRow
		var createdAt sql.NullTime
		if err := rows.Scan(&row.ID, &row.Name, &row.Slug, &row.Tier, &row.OwnerID, &row.OwnerEmail, &row.OwnerStatus,
			&row.ProjectCount, &row.RunningApps, &row.CPUCores, &row.MemoryMB, &createdAt, &page.Total); err != nil {
			return page, fmt.Errorf("scan org: %w", err)
		}
		row.CreatedAt = timePtr(createdAt)
		page.Items = append(page.Items, row)
	}
	return page, rows.Err()
}

func timePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}
