package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
)

var (
	_ adminconsole.AuditReader      = (*AdminConsole)(nil)
	_ adminconsole.UserDetailReader = (*AdminConsole)(nil)
)

// auditSelect is one audit row with the address behind its actor and, for an
// account, its target. Rows written through the hosted MCP endpoint (via <> ”)
// are a tenant's own tool calls and stay out of the platform's view.
const auditSelect = `
	SELECT a.id, a.timestamp, COALESCE(a.user_id, ''), COALESCE(au.email, ''), a.action,
	       COALESCE(a.resource, ''), COALESCE(a.resource_id, ''), COALESCE(tu.email, ''),
	       COALESCE(a.details, ''), COALESCE(a.ip_address, '')
	FROM audit_log a
	LEFT JOIN users au ON au.id = a.user_id
	LEFT JOIN users tu ON a.resource = 'user' AND tu.id = a.resource_id
	WHERE a.via = ''`

// The log is read by keyset on its serial id, never OFFSET: the page after a
// row is found by seeking below its id, so a deep page costs what the first
// does. The action filter is a prefix match by starts_with, so no wildcard in
// the request is ever interpreted.
const auditPageSQL = auditSelect + `
	  AND ($1::bigint = 0 OR a.id < $1)
	  AND ($2::text = '' OR a.user_id = $2 OR lower(au.email) = lower($2))
	  AND ($3::text = '' OR starts_with(a.action, $3))
	  AND ($4::text = '' OR a.resource_id = $4 OR a.project_id = $4)
	ORDER BY a.id DESC
	LIMIT $5`

// recentAuditForUserSQL is what the account page shows: entries the account made or that were made to it.
const recentAuditForUserSQL = auditSelect + `
	  AND (a.user_id = $1 OR (a.resource = 'user' AND a.resource_id = $1))
	ORDER BY a.id DESC
	LIMIT $2`

func scanAuditRows(rows *sql.Rows) ([]adminconsole.AuditRow, error) {
	out := []adminconsole.AuditRow{}
	for rows.Next() {
		var row adminconsole.AuditRow
		var id int64
		var at sql.NullTime
		if err := rows.Scan(&id, &at, &row.ActorID, &row.ActorEmail, &row.Action, &row.Resource, &row.ResourceID,
			&row.TargetEmail, &row.Details, &row.IP); err != nil {
			return nil, fmt.Errorf("scan audit: %w", err)
		}
		row.ID, row.At = strconv.FormatInt(id, 10), timePtr(at)
		out = append(out, row)
	}
	return out, rows.Err()
}

// ListAudit reads query.Limit rows below query.BeforeID, newest first, and says whether more remain.
func (a *AdminConsole) ListAudit(ctx context.Context, query adminconsole.AuditQuery) (adminconsole.AuditPage, error) {
	rows, err := a.db.QueryContext(ctx, auditPageSQL, query.BeforeID, query.Actor, query.Action, query.Target, query.Limit+1)
	if err != nil {
		return adminconsole.AuditPage{}, fmt.Errorf("audit page: %w", err)
	}
	defer rows.Close()
	items, err := scanAuditRows(rows)
	if err != nil {
		return adminconsole.AuditPage{}, err
	}
	page := adminconsole.AuditPage{Items: items}
	if len(items) > query.Limit {
		page.Items, page.More = items[:query.Limit], true
	}
	return page, nil
}
