package schema

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// ValidateTriggerTiming validates that the timing is a valid PostgreSQL trigger timing.
func ValidateTriggerTiming(timing string) error {
	switch strings.ToUpper(timing) {
	case "BEFORE", "AFTER", "INSTEAD OF":
		return nil
	default:
		return fmt.Errorf("invalid trigger timing: %q (must be BEFORE, AFTER, or INSTEAD OF)", timing)
	}
}

// ValidateTriggerEvent validates that the event is a valid PostgreSQL trigger event.
func ValidateTriggerEvent(event string) error {
	switch strings.ToUpper(event) {
	case "INSERT", "UPDATE", "DELETE", "TRUNCATE":
		return nil
	default:
		return fmt.Errorf("invalid trigger event: %q (must be INSERT, UPDATE, DELETE, or TRUNCATE)", event)
	}
}

// GetTriggers returns all triggers in the given schema.
func (i *Introspector) GetTriggers(ctx context.Context, db *sql.DB, schemaName string) ([]TriggerInfo, error) {
	rows, err := db.QueryContext(ctx, triggersQuery, schemaName)
	if err != nil {
		return nil, fmt.Errorf("query triggers: %w", err)
	}
	defer rows.Close()

	triggers := make([]TriggerInfo, 0)
	for rows.Next() {
		var t TriggerInfo
		var tgType int16
		var tgEnabled string
		if err := rows.Scan(&t.Name, &t.Table, &t.Schema, &t.Function, &tgType, &tgEnabled); err != nil {
			return nil, fmt.Errorf("scan trigger: %w", err)
		}
		decodeTriggerType(tgType, tgEnabled, &t)

		triggers = append(triggers, t)
	}
	return triggers, rows.Err()
}

// CreateTrigger creates a new trigger on a table.
func (i *Introspector) CreateTrigger(ctx context.Context, db *sql.DB, req CreateTriggerRequest) error {
	schemaName := req.Schema
	if schemaName == "" {
		schemaName = "public"
	}

	if err := ValidateTriggerTiming(req.Timing); err != nil {
		return err
	}
	if err := ValidateTriggerEvent(req.Event); err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("CREATE TRIGGER ")
	b.WriteString(QuoteIdent(req.Name))
	b.WriteString(" ")
	b.WriteString(strings.ToUpper(req.Timing))
	b.WriteString(" ")
	b.WriteString(strings.ToUpper(req.Event))
	b.WriteString(" ON ")
	b.WriteString(QuoteIdent(schemaName))
	b.WriteString(".")
	b.WriteString(QuoteIdent(req.Table))

	if req.ForEachRow {
		b.WriteString(" FOR EACH ROW")
	} else {
		b.WriteString(" FOR EACH STATEMENT")
	}

	b.WriteString(" EXECUTE FUNCTION ")
	b.WriteString(QuoteIdent(schemaName))
	b.WriteString(".")
	b.WriteString(QuoteIdent(req.Function))
	b.WriteString("()")

	if _, err := db.ExecContext(ctx, b.String()); err != nil {
		return fmt.Errorf("create trigger: %w", err)
	}
	return nil
}

// DropTrigger drops a trigger from a table.
func (i *Introspector) DropTrigger(ctx context.Context, db *sql.DB, schema, table, name string) error {
	stmt := "DROP TRIGGER " + QuoteIdent(name) + " ON " + QuoteIdent(schema) + "." + QuoteIdent(table)
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("drop trigger: %w", err)
	}
	return nil
}

const triggersQuery = `
SELECT
    t.tgname AS name,
    c.relname AS table_name,
    n.nspname AS schema_name,
    p.proname AS function_name,
    t.tgtype,
    t.tgenabled
FROM pg_trigger t
JOIN pg_class c ON c.oid = t.tgrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_proc p ON p.oid = t.tgfoid
WHERE n.nspname = $1
AND NOT t.tgisinternal
ORDER BY c.relname, t.tgname`

// decodeTriggerType fills TriggerInfo fields from the PostgreSQL tgtype bitmask.
// tgtype bit layout: bit 0 = FOR EACH ROW, bit 1 = BEFORE, bit 6 = INSTEAD OF,
// bits 2-5 = INSERT/DELETE/UPDATE/TRUNCATE respectively.
func decodeTriggerType(tgType int16, tgEnabled string, t *TriggerInfo) {
	t.ForEachRow = tgType&1 != 0

	switch {
	case tgType&(1<<1) != 0:
		t.Timing = "BEFORE"
	case tgType&(1<<6) != 0:
		t.Timing = "INSTEAD OF"
	default:
		t.Timing = "AFTER"
	}

	events := make([]string, 0, 4)
	if tgType&(1<<2) != 0 {
		events = append(events, "INSERT")
	}
	if tgType&(1<<3) != 0 {
		events = append(events, "DELETE")
	}
	if tgType&(1<<4) != 0 {
		events = append(events, "UPDATE")
	}
	if tgType&(1<<5) != 0 {
		events = append(events, "TRUNCATE")
	}
	t.Event = strings.Join(events, ",")

	// tgenabled: 'O' = origin (default enabled), 'D' = disabled, 'A' = always, 'R' = replica
	t.Enabled = tgEnabled != "D"
}
