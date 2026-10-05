package schema

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
)

// MaxReadOnlySQL bounds a read-only statement, which travels in a query string.
const MaxReadOnlySQL = 16 << 10

// sideEffectFunctions act outside the transaction that runs them: they signal
// other sessions, write over another connection, touch server files or
// notify listeners, so a read-only transaction does not contain them.
var sideEffectFunctions = regexp.MustCompile(`(?i)"?\b(pg_terminate_backend|pg_cancel_backend|pg_reload_conf|` +
	`pg_rotate_logfile|pg_promote|pg_switch_wal|pg_create_restore_point|pg_notify|dblink\w*|lo_\w+)\b"?\s*\(`)

// quotedText is a string literal, a dollar-quoted body or a comment: text the
// function check must not read as a call.
var quotedText = regexp.MustCompile(`(?s)'(?:[^']|'')*'|\$([A-Za-z_]*)\$.*?\$[A-Za-z_]*\$|--[^\n]*|/\*.*?\*/`)

// sideEffectCall names the first side-effect function the statement calls,
// or "" when it calls none.
func sideEffectCall(query string) string {
	match := sideEffectFunctions.FindStringSubmatch(quotedText.ReplaceAllString(query, " "))
	if match == nil {
		return ""
	}
	return strings.ToLower(match[1])
}

// ExecuteReadOnlyQuery runs one statement in a read-only transaction that is
// always rolled back (EXC-544). The statement is prepared, which refuses a
// second one, so the SQL cannot end the transaction and write in a new one;
// it runs on a connection that is thrown away afterwards, so nothing it set
// on the session (an advisory lock, a setting) outlives the call.
func (i *Introspector) ExecuteReadOnlyQuery(ctx context.Context, db *sql.DB, query string) QueryResult {
	if name := sideEffectCall(query); name != "" {
		return QueryResult{Error: fmt.Sprintf("read-only SQL may not call %s", name)}
	}
	ctx, cancel := i.bounded(ctx)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		return QueryResult{Error: fmt.Errorf("open connection: %w", err).Error()}
	}
	defer discard(conn)
	return i.readOnly(ctx, conn, query)
}

func (i *Introspector) readOnly(ctx context.Context, conn *sql.Conn, query string) QueryResult {
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return QueryResult{Error: fmt.Errorf("begin tx: %w", err).Error()}
	}
	// Always rolled back: nothing a read-only call runs is ever committed.
	defer tx.Rollback()
	timeout := fmt.Sprintf("SET LOCAL statement_timeout = %d", i.statementTimeout.Milliseconds())
	if _, err := tx.ExecContext(ctx, timeout); err != nil {
		return QueryResult{Error: fmt.Errorf("set timeout: %w", err).Error()}
	}
	statement, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	defer statement.Close()
	rows, err := statement.QueryContext(ctx)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	return readResult(rows)
}

// discard closes the connection instead of returning it to the pool.
func discard(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	if err := conn.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
		log.Printf("read-only query: close connection: %v", err)
	}
}
