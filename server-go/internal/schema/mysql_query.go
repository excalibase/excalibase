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

// mysqlScriptReturnsRows says whether any statement of a script reads, so
// its rows are kept rather than discarded by an Exec.
func mysqlScriptReturnsRows(query string) bool {
	for _, statement := range mysqlStatements(query) {
		if mysqlReads[mysqlLeadingWord(statement)] {
			return true
		}
	}
	return false
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
	defer m.release(ctx, db, conn, connectionID)
	if mysqlScriptReturnsRows(query) {
		return readScript(ctx, conn, query)
	}
	affected, err := execScript(ctx, conn, query)
	if err != nil {
		return QueryResult{Error: mysqlMessage(err)}
	}
	return QueryResult{Command: "EXEC", AffectedRows: affected}
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
	defer m.release(ctx, db, conn, connectionID)
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

// release discards the connection, and when the call ended early (its
// deadline passed or the client left) stops its statement on the server: the
// driver only drops the connection, and MySQL would keep running what it was
// sent. The connection is released first so the kill never waits behind it.
func (m *MySQLCatalog) release(ctx context.Context, db *sql.DB, conn *sql.Conn, connectionID int64) {
	discard(conn)
	if ctx.Err() == nil {
		return
	}
	killCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), killGrace)
	defer cancel()
	if err := m.kill(killCtx, db, connectionID); err != nil {
		log.Printf("mysql: stop a statement its caller abandoned: %v", err)
	}
}

// MySQLKiller stops a server thread's statement. The platform's sends the
// KILL on a connection outside the pool, which a runaway may have filled.
type MySQLKiller func(ctx context.Context, db *sql.DB, threadID int64) error

// WithKiller sets how abandoned statements are stopped.
func (m *MySQLCatalog) WithKiller(kill MySQLKiller) *MySQLCatalog {
	m.kill = kill
	return m
}

// killThroughPool is the default killer, for a pool with room to spare.
func killThroughPool(ctx context.Context, db *sql.DB, threadID int64) error {
	_, err := db.ExecContext(ctx, fmt.Sprintf("KILL QUERY %d", threadID))
	return err
}

// KillMySQLQuery stops a thread's statement over a new connection from the
// connector, outside any pool.
func KillMySQLQuery(ctx context.Context, connector driver.Connector, threadID int64) error {
	conn, err := connector.Connect(ctx)
	if err != nil {
		return fmt.Errorf("connect to stop a statement: %w", err)
	}
	defer conn.Close()
	execer, ok := conn.(driver.ExecerContext)
	if !ok {
		return errors.New("the MySQL driver cannot send KILL")
	}
	_, err = execer.ExecContext(ctx, fmt.Sprintf("KILL QUERY %d", threadID), nil)
	return err
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

// execScript runs a script for its effect and adds up every statement's
// count; database/sql's result shows only the last one, so the driver's own
// result is read.
func execScript(ctx context.Context, conn *sql.Conn, query string) (int64, error) {
	var total int64
	err := conn.Raw(func(driverConn any) error {
		execer, ok := driverConn.(driver.ExecerContext)
		if !ok {
			return errors.New("the MySQL driver cannot run a script")
		}
		result, err := execer.ExecContext(ctx, query, nil)
		if err != nil {
			return err
		}
		counts := []int64{}
		if all, ok := result.(mysql.Result); ok {
			counts = all.AllRowsAffected()
		} else if count, err := result.RowsAffected(); err == nil {
			counts = append(counts, count)
		}
		for _, count := range counts {
			total += count
		}
		return nil
	})
	return total, err
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
