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
type AdminConsole struct {
	db *sql.DB
	// netSamples says whether the net meter is measuring: then the top-egress
	// card reads its table instead of single-host monitoring's, so one source
	// answers it (EXC-844). Asked on every read, so it follows the meter.
	netSamples func() bool
}

// NewAdminConsole serves the console from the platform database's connection.
func NewAdminConsole(db *sql.DB) *AdminConsole { return &AdminConsole{db: db} }

// WithNetSamples returns the console reading egress from the net meter's samples while it reports true.
func (a *AdminConsole) WithNetSamples(measuring func() bool) *AdminConsole {
	copied := *a
	copied.netSamples = measuring
	return &copied
}

// ReadsNetSamples is whether the top-egress card reads the net meter's samples now.
func (a *AdminConsole) ReadsNetSamples() bool { return a.netSamples != nil && a.netSamples() }

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

// ListUsers pages the accounts. A page past the end has no rows to carry the
// count, so the total is read from the first row of the same filter.
func (a *AdminConsole) ListUsers(ctx context.Context, q adminconsole.UserQuery) (adminconsole.Page[adminconsole.UserRow], error) {
	page, err := a.listUsers(ctx, q)
	if err == nil && len(page.Items) == 0 && q.Offset > 0 {
		q.Limit, q.Offset = 1, 0
		first, firstErr := a.listUsers(ctx, q)
		page.Total = first.Total
		err = firstErr
	}
	return page, err
}

func (a *AdminConsole) listUsers(ctx context.Context, q adminconsole.UserQuery) (adminconsole.Page[adminconsole.UserRow], error) {
	page := adminconsole.Page[adminconsole.UserRow]{Items: []adminconsole.UserRow{}}
	query, ok := listUsersSQL[sortKey{q.Sort, q.Desc}]
	if !ok {
		return page, fmt.Errorf("list users: unknown sort %q", q.Sort)
	}
	rows, err := a.db.QueryContext(ctx, query,
		apphost.StatusRunning, q.IncludeSuspended, escapeLike(q.Search), strings.TrimSpace(q.Search), q.Limit, q.Offset, q.Status, q.Role)
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

// ListOrgs pages the organizations, with the same total past the end as ListUsers.
func (a *AdminConsole) ListOrgs(ctx context.Context, q adminconsole.OrgQuery) (adminconsole.Page[adminconsole.OrgRow], error) {
	page, err := a.listOrgs(ctx, q)
	if err == nil && len(page.Items) == 0 && q.Offset > 0 {
		q.Limit, q.Offset = 1, 0
		first, firstErr := a.listOrgs(ctx, q)
		page.Total = first.Total
		err = firstErr
	}
	return page, err
}

func (a *AdminConsole) listOrgs(ctx context.Context, q adminconsole.OrgQuery) (adminconsole.Page[adminconsole.OrgRow], error) {
	page := adminconsole.Page[adminconsole.OrgRow]{Items: []adminconsole.OrgRow{}}
	query, ok := listOrgsSQL[sortKey{q.Sort, q.Desc}]
	if !ok {
		return page, fmt.Errorf("list orgs: unknown sort %q", q.Sort)
	}
	rows, err := a.db.QueryContext(ctx, query,
		apphost.StatusRunning, q.IncludeSuspended, escapeLike(q.Search), q.Limit, q.Offset, q.Tier, q.OwnerStatus)
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

// sortKey names one ordering of a list. A request's sort is only ever looked up
// in the maps below, which hold the finished statements.
type sortKey struct {
	name string
	desc bool
}

// orderedQueries builds one statement per allowed ordering from constant
// fragments at start-up. The default ("") is newest first whatever the direction.
func orderedQueries(base string, orders map[string]string, tail string) map[sortKey]string {
	queries := map[sortKey]string{}
	for name, column := range orders {
		queries[sortKey{name, false}] = base + " ORDER BY " + column + " ASC NULLS LAST, " + tail
		queries[sortKey{name, true}] = base + " ORDER BY " + column + " DESC NULLS LAST, " + tail
	}
	queries[sortKey{"", false}] = queries[sortKey{"created", true}]
	queries[sortKey{"", true}] = queries[sortKey{"created", true}]
	return queries
}

// userOrders and orgOrders are the columns each table may be sorted by.
var userOrders = map[string]string{
	"created":   "u.created_at",
	"email":     "lower(u.email)",
	"lastLogin": "u.last_login_at",
	"orgs":      "COALESCE(owned.orgs, 0)",
	"projects":  "COALESCE(owned.projects, 0)",
	"apps":      "COALESCE(running.apps, 0)",
}

var orgOrders = map[string]string{
	"created":  "o.created_at",
	"name":     "lower(o.name)",
	"owner":    "lower(u.email)",
	"plan":     "o.tier",
	"projects": "COALESCE(per_project.projects, 0)",
	"apps":     "COALESCE(per_app.running, 0)",
}

var (
	listUsersSQL = orderedQueries(listUsersBase, userOrders, "u.id LIMIT $5 OFFSET $6")
	listOrgsSQL  = orderedQueries(listOrgsBase, orgOrders, "o.id LIMIT $4 OFFSET $5")
)

// listUsersBase is composed at compile time from constant fragments.
const listUsersBase = `
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
		SELECT u.id, u.email, u.username, u.role, ` + statusExpr + `,
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
		  AND ($7 = '' OR ` + statusExpr + ` = $7)
		  AND ($8 = '' OR u.role = $8)`

// listOrgsBase is composed at compile time from constant fragments.
const listOrgsBase = `
		WITH usage AS (` + latestMetricsSQL + `),
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
		SELECT o.id, o.name, o.slug, o.tier, o.owner_id, COALESCE(u.email, ''), ` + statusExpr + `,
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
		  AND ($6 = '' OR upper(o.tier) = $6)
		  AND ($7 = '' OR ` + statusExpr + ` = $7)`
