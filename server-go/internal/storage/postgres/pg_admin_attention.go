package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
)

var _ adminconsole.AttentionReader = (*AdminConsole)(nil)

// Both lists are read by keyset, never OFFSET: the page after a row is found
// by seeking past its (time, id), so a deep page costs what the first does and
// rows suspended while someone scrolls cannot shift a page.
const (
	suspendedPageSQL = `
		SELECT user_id, COALESCE(email, ''), COALESCE(reason, ''), suspended_at
		FROM abuse_suspensions
		WHERE suspended_at > $1
		  AND ($2::timestamptz IS NULL OR (suspended_at, user_id) < ($2::timestamptz, $3::text))
		ORDER BY suspended_at DESC, user_id DESC
		LIMIT $4`

	suspendedTotalSQL = `SELECT COUNT(*) FROM abuse_suspensions WHERE suspended_at > $1`

	failedFilter = `di.status = 'FAILED' OR di.current_stage = 'FAILED'`

	// Projects with no updated_at sort last, so after a row that has one they all
	// still come, and after one that has none only the smaller ids do.
	failedPageSQL = `
		WITH usage AS (` + latestMetricsSQL + `)
		SELECT ` + projectBriefColumns + `, di.updated_at` + projectBriefFrom + `
		WHERE (` + failedFilter + `)
		  AND (NOT $1::bool
		       OR ($2::timestamptz IS NOT NULL AND (di.updated_at IS NULL OR (di.updated_at, di.project_id) < ($2::timestamptz, $3::text)))
		       OR ($2::timestamptz IS NULL AND di.updated_at IS NULL AND di.project_id < $3::text))
		ORDER BY di.updated_at DESC NULLS LAST, di.project_id DESC
		LIMIT $4`

	failedTotalSQL = `SELECT COUNT(*) FROM database_instances di WHERE ` + failedFilter
)

// cursorArgs are a cursor's time and id as query arguments; a nil time is SQL NULL.
func cursorArgs(after *adminconsole.Cursor) (any, string) {
	if after == nil || after.At == nil {
		if after == nil {
			return nil, ""
		}
		return nil, after.ID
	}
	return *after.At, after.ID
}

// ListSuspended pages the accounts suspended since query.Since, newest first.
func (a *AdminConsole) ListSuspended(ctx context.Context, query adminconsole.AttentionQuery) (adminconsole.CursorPage[adminconsole.SuspendedBrief], error) {
	page := adminconsole.CursorPage[adminconsole.SuspendedBrief]{Items: []adminconsole.SuspendedBrief{}}
	if err := a.db.QueryRowContext(ctx, suspendedTotalSQL, query.Since).Scan(&page.Total); err != nil {
		return page, fmt.Errorf("count suspended: %w", err)
	}
	at, id := cursorArgs(query.After)
	rows, err := a.db.QueryContext(ctx, suspendedPageSQL, query.Since, at, id, query.Limit+1)
	if err != nil {
		return page, fmt.Errorf("suspended page: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var brief adminconsole.SuspendedBrief
		var suspendedAt sql.NullTime
		if err := rows.Scan(&brief.UserID, &brief.Email, &brief.Reason, &suspendedAt); err != nil {
			return page, err
		}
		brief.SuspendedAt = suspendedAt.Time
		page.Items = append(page.Items, brief)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Items) > query.Limit {
		page.Items = page.Items[:query.Limit]
		last := page.Items[query.Limit-1]
		page.NextCursor = adminconsole.EncodeCursor(adminconsole.Cursor{At: &last.SuspendedAt, ID: last.UserID})
	}
	return page, nil
}

// ListFailedProjects pages the failed projects, most recently changed first.
func (a *AdminConsole) ListFailedProjects(ctx context.Context, query adminconsole.AttentionQuery) (adminconsole.CursorPage[adminconsole.ProjectBrief], error) {
	page := adminconsole.CursorPage[adminconsole.ProjectBrief]{Items: []adminconsole.ProjectBrief{}}
	if err := a.db.QueryRowContext(ctx, failedTotalSQL).Scan(&page.Total); err != nil {
		return page, fmt.Errorf("count failed projects: %w", err)
	}
	at, id := cursorArgs(query.After)
	rows, err := a.db.QueryContext(ctx, failedPageSQL, query.After != nil, at, id, query.Limit+1)
	if err != nil {
		return page, fmt.Errorf("failed projects page: %w", err)
	}
	defer rows.Close()
	var changed []sql.NullTime
	for rows.Next() {
		var brief adminconsole.ProjectBrief
		var updated sql.NullTime
		if err := rows.Scan(&brief.ProjectID, &brief.ProjectName, &brief.OrgID, &brief.OwnerEmail, &brief.OwnerStatus,
			&brief.Status, &brief.FailureReason, &brief.CPUCores, &brief.MemoryMB, &updated); err != nil {
			return page, err
		}
		page.Items = append(page.Items, brief)
		changed = append(changed, updated)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Items) > query.Limit {
		page.Items = page.Items[:query.Limit]
		cursor := adminconsole.Cursor{ID: page.Items[query.Limit-1].ProjectID}
		if last := changed[query.Limit-1]; last.Valid {
			cursor.At = &last.Time
		}
		page.NextCursor = adminconsole.EncodeCursor(cursor)
	}
	return page, nil
}
