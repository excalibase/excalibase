package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/lib/pq"
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
// does. The action filter is an anchored LIKE with the request's wildcards
// escaped, so it is a prefix match the text_pattern_ops index serves, and the
// actor is resolved to account ids first so the user_id index serves it too.
const auditPageSQL = auditSelect + `
	  AND ($1::bigint = 0 OR a.id < $1)
	  AND ($2::text[] IS NULL OR a.user_id = ANY($2))
	  AND ($3::text = '' OR a.action LIKE $3 || '%')
	  AND ($4::text = '' OR a.resource_id = $4 OR a.project_id = $4)
	ORDER BY a.id DESC
	LIMIT $5`

// auditActorIDs is every account id the actor filter names: the value itself
// (an account that is gone still has a trail) and the accounts with that address.
const auditActorIDsSQL = `SELECT id FROM users WHERE lower(email) = lower($1)`

// auditTimeout bounds one page read so a filter on a huge log cannot hold a connection.
const auditTimeout = "5000"

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

// actorIDs turns the actor filter into the account ids it names; nil means no filter.
func (a *AdminConsole) actorIDs(ctx context.Context, tx *sql.Tx, actor string) ([]string, error) {
	if actor == "" {
		return nil, nil
	}
	ids := []string{actor}
	rows, err := tx.QueryContext(ctx, auditActorIDsSQL, actor)
	if err != nil {
		return nil, fmt.Errorf("resolve actor: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan actor: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListAudit reads query.Limit rows below query.BeforeID, newest first, and says whether more remain.
func (a *AdminConsole) ListAudit(ctx context.Context, query adminconsole.AuditQuery) (adminconsole.AuditPage, error) {
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return adminconsole.AuditPage{}, fmt.Errorf("audit page: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('statement_timeout', $1, true)`, auditTimeout); err != nil {
		return adminconsole.AuditPage{}, fmt.Errorf("audit page: %w", err)
	}
	actors, err := a.actorIDs(ctx, tx, query.Actor)
	if err != nil {
		return adminconsole.AuditPage{}, err
	}
	rows, err := tx.QueryContext(ctx, auditPageSQL, query.BeforeID, pq.Array(actors), escapeLike(query.Action), query.Target, query.Limit+1)
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
