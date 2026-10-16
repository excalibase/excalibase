package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// killGrace bounds the KILL QUERY sent for a statement past its deadline.
const killGrace = 5 * time.Second

var (
	// leadingComments are the comments a statement may open with.
	leadingComments = regexp.MustCompile(`^(?:\s+|--[^\n]*\n?|#[^\n]*\n?|/\*.*?\*/)*`)
	firstWord       = regexp.MustCompile(`^[A-Za-z]+`)
	// fileWrites are a SELECT's ways of writing a server file.
	fileWrites = regexp.MustCompile(`(?i)\binto\s+(?:outfile|dumpfile)\b`)
	mysqlReads = map[string]bool{"SELECT": true, "WITH": true, "SHOW": true, "EXPLAIN": true, "DESCRIBE": true, "DESC": true, "TABLE": true, "VALUES": true}
)

// mysqlLeadingWord is the statement's first keyword, upper case.
func mysqlLeadingWord(query string) string {
	rest := query[len(leadingComments.FindString(query)):]
	return strings.ToUpper(firstWord.FindString(rest))
}

// mysqlScriptReturnsRows says whether a script's first statement reads.
func mysqlScriptReturnsRows(query string) bool {
	return mysqlReads[mysqlLeadingWord(query)]
}

// mysqlReadOnlyRefusal is why a read-only call refuses the statement, or "".
// The READ ONLY transaction refuses writes; this also keeps out what it does
// not (temporary tables, session settings, file writes, procedures).
func mysqlReadOnlyRefusal(query string) string {
	if !mysqlReads[mysqlLeadingWord(query)] {
		return "read-only SQL on MySQL runs SELECT, WITH, SHOW, EXPLAIN, DESCRIBE, TABLE or VALUES"
	}
	if fileWrites.MatchString(quotedText.ReplaceAllString(query, " ")) {
		return "read-only SQL may not write a file"
	}
	return ""
}

// ExecuteQuery runs a script of one or more statements on a pool that allows
// several (projectdb script pool). MySQL commits each DDL statement as it
// runs, so a script that fails part way keeps what ran before the failure.
// The connection is discarded afterwards: nothing the script set on its
// session reaches the next caller.
func (m *MySQLCatalog) ExecuteQuery(ctx context.Context, db *sql.DB, query string) QueryResult {
	result := m.runScript(ctx, db, query)
	result.Notice = m.scriptNotice()
	return result
}

// scriptNotice is said with every script's answer, failed or not.
func (m *MySQLCatalog) scriptNotice() string {
	return fmt.Sprintf("MySQL DDL is not transactional: each statement commits as it runs, so statements before a failing one "+
		"stay applied and a failed migration is not rolled back. SELECTs stop after max_execution_time (%s); "+
		"any statement is stopped after %s.", m.statementTimeout, m.statementTimeout+cancelGrace)
}

func (m *MySQLCatalog) runScript(ctx context.Context, db *sql.DB, query string) QueryResult {
	ctx, cancel := context.WithTimeout(ctx, m.statementTimeout+cancelGrace)
	defer cancel()
	conn, connectionID, err := m.session(ctx, db)
	if err != nil {
		return QueryResult{Error: mysqlMessage(err)}
	}
	defer discard(conn)
	defer killIfPastDeadline(ctx, db, connectionID)
	if mysqlScriptReturnsRows(query) {
		return readScript(ctx, conn, query)
	}
	executed, err := conn.ExecContext(ctx, query)
	if err != nil {
		return QueryResult{Error: mysqlMessage(err)}
	}
	return QueryResult{Command: "EXEC", AffectedRows: affectedRows(executed)}
}

// ExecuteDDL runs a script for its effect.
func (m *MySQLCatalog) ExecuteDDL(ctx context.Context, db *sql.DB, ddl string) DDLResult {
	result := m.ExecuteQuery(ctx, db, ddl)
	if result.Error != "" {
		return DDLResult{Success: false, Error: result.Error}
	}
	return DDLResult{Success: true, UpdateCount: int(result.AffectedRows)}
}

// ExecuteReadOnlyQuery runs one statement in a READ ONLY transaction that is
// always rolled back, on a single-statement pool, as a prepared statement
// (EXC-544's rules on MySQL).
func (m *MySQLCatalog) ExecuteReadOnlyQuery(ctx context.Context, db *sql.DB, query string) QueryResult {
	if reason := mysqlReadOnlyRefusal(query); reason != "" {
		return QueryResult{Error: reason}
	}
	ctx, cancel := context.WithTimeout(ctx, m.statementTimeout+cancelGrace)
	defer cancel()
	conn, connectionID, err := m.session(ctx, db)
	if err != nil {
		return QueryResult{Error: mysqlMessage(err)}
	}
	defer discard(conn)
	defer killIfPastDeadline(ctx, db, connectionID)
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return QueryResult{Error: mysqlMessage(fmt.Errorf("begin tx: %w", err))}
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return QueryResult{Error: mysqlMessage(err)}
	}
	defer statement.Close()
	rows, err := statement.QueryContext(ctx)
	if err != nil {
		return QueryResult{Error: mysqlMessage(err)}
	}
	return readResult(rows)
}

// session takes a connection of its own and its server thread id, which a
// KILL QUERY needs.
func (m *MySQLCatalog) session(ctx context.Context, db *sql.DB) (*sql.Conn, int64, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("open connection: %w", err)
	}
	var connectionID int64
	if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&connectionID); err != nil {
		discard(conn)
		return nil, 0, fmt.Errorf("open connection: %w", err)
	}
	return conn, connectionID, nil
}

// killIfPastDeadline stops a statement the deadline abandoned: the driver
// only drops the connection, and MySQL would keep running what it was sent.
func killIfPastDeadline(ctx context.Context, db *sql.DB, connectionID int64) {
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return
	}
	killCtx, cancel := context.WithTimeout(context.Background(), killGrace)
	defer cancel()
	if _, err := db.ExecContext(killCtx, fmt.Sprintf("KILL QUERY %d", connectionID)); err != nil {
		log.Printf("mysql: stop statement past its deadline: %v", err)
	}
}

// readScript keeps the last result set that has columns.
func readScript(ctx context.Context, conn *sql.Conn, query string) QueryResult {
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return QueryResult{Error: mysqlMessage(err)}
	}
	defer rows.Close()
	last := QueryResult{Command: "EXEC"}
	for {
		columns, err := rows.Columns()
		if err != nil {
			return QueryResult{Error: mysqlMessage(err)}
		}
		if len(columns) > 0 {
			if last, err = readSet(rows); err != nil {
				return QueryResult{Error: mysqlMessage(err)}
			}
		}
		if !rows.NextResultSet() {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return QueryResult{Error: mysqlMessage(err)}
	}
	return last
}

func readSet(rows *sql.Rows) (QueryResult, error) {
	types, err := rows.ColumnTypes()
	if err != nil {
		return QueryResult{}, err
	}
	columns := make([]ColumnMeta, len(types))
	for index, columnType := range types {
		columns[index] = ColumnMeta{Name: columnType.Name(), DataType: columnType.DatabaseTypeName()}
	}
	kept, truncated, err := collectRows(rows, len(types))
	if err != nil {
		return QueryResult{}, err
	}
	// The rest of a truncated set is skipped by NextResultSet.
	return QueryResult{Columns: columns, Rows: kept, Truncated: truncated}, nil
}

// affectedRows adds up every statement's count in a script.
func affectedRows(result sql.Result) int64 {
	if all, ok := result.(interface{ AllRowsAffected() []int64 }); ok {
		var total int64
		for _, count := range all.AllRowsAffected() {
			total += count
		}
		return total
	}
	count, _ := result.RowsAffected()
	return count
}

// mysqlMessage is a MySQL refusal as the user reads it, with its error number.
func mysqlMessage(err error) string {
	var refusal *mysql.MySQLError
	if errors.As(err, &refusal) {
		return fmt.Sprintf("%s (MySQL error %d)", refusal.Message, refusal.Number)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "the statement ran past its time limit and was stopped"
	}
	return err.Error()
}
