package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/excalibase/provisioning-poc/internal/credits"
)

const creditEntryColumns = `SELECT id, org_id, kind, amount_micros, idempotency_key, period_start, period_end, actor, reason, details, created_at`

// Each entry query is one constant, so what reaches the database is never built at the call site.
const (
	creditEntriesPageSQL    = creditEntryColumns + ` FROM credit_entries WHERE org_id = $1 AND ($2 = 0 OR id < $2) ORDER BY id DESC LIMIT $3`
	creditStatementEntrySQL = creditEntryColumns + ` FROM credit_entries WHERE org_id = $1 AND COALESCE(period_start, created_at) >= $2 AND COALESCE(period_start, created_at) < $3 ORDER BY COALESCE(period_start, created_at), id LIMIT $4`
	creditEntryByKeySQL     = creditEntryColumns + ` FROM credit_entries WHERE idempotency_key = $1`
)

func scanCreditEntry(row pgRowScanner) (credits.Entry, error) {
	var entry credits.Entry
	var amount int64
	var start, end sql.NullTime
	var details []byte
	if err := row.Scan(&entry.ID, &entry.OrgID, &entry.Kind, &amount, &entry.Key, &start, &end, &entry.Actor, &entry.Reason, &details, &entry.CreatedAt); err != nil {
		return credits.Entry{}, err
	}
	entry.Amount = credits.Micros(amount)
	entry.CreatedAt = entry.CreatedAt.UTC()
	if start.Valid {
		at := start.Time.UTC()
		entry.PeriodStart = &at
	}
	if end.Valid {
		at := end.Time.UTC()
		entry.PeriodEnd = &at
	}
	entry.Details = map[string]any{}
	if len(details) > 0 {
		if err := json.Unmarshal(details, &entry.Details); err != nil {
			return credits.Entry{}, err
		}
	}
	return entry, nil
}

func scanCreditEntries(rows *sql.Rows) ([]credits.Entry, error) {
	defer rows.Close()
	entries := []credits.Entry{}
	for rows.Next() {
		entry, err := scanCreditEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// CreditEntries lists an org's entries newest first, those before beforeID when it is set.
func (s *Store) CreditEntries(ctx context.Context, orgID string, beforeID int64, limit int) ([]credits.Entry, error) {
	rows, err := s.db.QueryContext(ctx, creditEntriesPageSQL, orgID, beforeID, limit)
	if err != nil {
		return nil, err
	}
	return scanCreditEntries(rows)
}

// CreditStatement is one UTC month. A usage line belongs to the month it was
// used in, whenever it was recorded; every other line to the month it was made.
// It gives the balance when it opened, its entries
// oldest first, and the balance when it closed.
func (s *Store) CreditStatement(ctx context.Context, orgID string, month time.Time) (credits.Statement, error) {
	month = month.UTC()
	start := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	statement := credits.Statement{Month: start.Format("2006-01")}
	var opening int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_micros), 0) FROM credit_entries WHERE org_id = $1 AND COALESCE(period_start, created_at) < $2`, orgID, start).Scan(&opening); err != nil {
		return statement, err
	}
	rows, err := s.db.QueryContext(ctx, creditStatementEntrySQL, orgID, start, end, creditStatementEntryCap)
	if err != nil {
		return statement, err
	}
	statement.Entries, err = scanCreditEntries(rows)
	if err != nil {
		return statement, err
	}
	statement.Opening = credits.Micros(opening)
	statement.Closing = statement.Opening
	for _, entry := range statement.Entries {
		statement.Closing += entry.Amount
	}
	return statement, nil
}
