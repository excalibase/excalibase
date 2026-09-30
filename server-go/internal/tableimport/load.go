package tableimport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
)

// Loader writes a validated import in one transaction: the table (for a
// create) and every row, or nothing.
type Loader struct {
	StatementTimeout time.Duration
	LockTimeout      time.Duration
	// ChunkRows rows go into one COPY statement, so the statement timeout
	// bounds a chunk rather than the whole file.
	ChunkRows int
	// MaxRowErrors is how many bad rows are collected before giving up.
	MaxRowErrors int
}

// Result reports a finished load.
type Result struct {
	Rows int `json:"rows"`
}

// Load reads src to the end. Row errors are gathered (up to MaxRowErrors)
// and refuse the whole load; nothing is committed unless every row fits.
func (l Loader) Load(ctx context.Context, db *sql.DB, opts Options, src RecordReader) (Result, error) {
	header, width, err := l.readHeader(src, opts)
	if err != nil {
		return Result{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := l.prepareTable(ctx, tx, opts); err != nil {
		return Result{}, err
	}
	batch := &copyBatch{tx: tx, opts: opts, chunkRows: l.chunkRows()}
	rows, err := l.copyRows(ctx, batch, src, newConverter(opts, width), header)
	if err != nil {
		return Result{}, err
	}
	if err := tx.Commit(); err != nil {
		return Result{}, classify(err, nil)
	}
	return Result{Rows: rows}, nil
}

func (l Loader) chunkRows() int {
	if l.ChunkRows <= 0 {
		return 20000
	}
	return l.ChunkRows
}

// readHeader consumes the header (or peeks the first data row) to learn the
// width rows are checked against.
func (l Loader) readHeader(src RecordReader, opts Options) (*pendingRow, int, error) {
	record, line, err := src.Next()
	if errors.Is(err, io.EOF) {
		return nil, 0, ErrEmptyFile
	}
	if err != nil {
		return nil, 0, err
	}
	width := max(len(trimTrailingEmpty(record)), maxSource(opts)+1)
	if opts.HasHeader {
		return nil, width, nil
	}
	return &pendingRow{record: record, line: line}, width, nil
}

type pendingRow struct {
	record []string
	line   int
}

func maxSource(opts Options) int {
	highest := 0
	for _, col := range opts.Columns {
		highest = max(highest, col.Source)
	}
	return highest
}

func (l Loader) prepareTable(ctx context.Context, tx *sql.Tx, opts Options) error {
	// set_config(..., true) is SET LOCAL: it ends with the transaction.
	if _, err := tx.ExecContext(ctx, "SELECT set_config('statement_timeout', $1, true), set_config('lock_timeout', $2, true)",
		strconv.FormatInt(l.StatementTimeout.Milliseconds(), 10), strconv.FormatInt(l.LockTimeout.Milliseconds(), 10)); err != nil {
		return fmt.Errorf("set timeouts: %w", err)
	}
	if opts.Mode == ModeCreate {
		if _, err := tx.ExecContext(ctx, opts.CreateTableSQL()); err != nil {
			return classify(err, nil)
		}
		return nil
	}
	return checkAppendTarget(ctx, tx, opts)
}

func checkAppendTarget(ctx context.Context, tx *sql.Tx, opts Options) error {
	rows, err := tx.QueryContext(ctx, `SELECT column_name FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2`, opts.Schema, opts.Table)
	if err != nil {
		return classify(err, nil)
	}
	defer rows.Close()
	existing := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(existing) == 0 {
		return ErrTableMissing
	}
	var missing []string
	for _, col := range opts.Columns {
		if !existing[col.Name] {
			missing = append(missing, col.Name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w %s", ErrColumnsMissing, strings.Join(missing, ", "))
	}
	return nil
}

func (l Loader) copyRows(ctx context.Context, batch *copyBatch, src RecordReader, conv converter, first *pendingRow) (int, error) {
	var rowErrors []RowError
	loaded := 0
	next := func() ([]string, int, error) {
		if first != nil {
			row := first
			first = nil
			return row.record, row.line, nil
		}
		return src.Next()
	}
	for len(rowErrors) < l.maxRowErrors() {
		record, line, err := next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, err
		}
		values, rowErr := conv.convert(record, line)
		if rowErr != nil {
			rowErrors = append(rowErrors, *rowErr)
			continue
		}
		if len(rowErrors) > 0 {
			continue
		}
		if err := batch.add(ctx, values, line); err != nil {
			return 0, err
		}
		loaded++
	}
	if len(rowErrors) > 0 {
		return 0, &RowErrors{Errors: rowErrors}
	}
	if err := batch.flush(ctx); err != nil {
		return 0, err
	}
	return loaded, nil
}

func (l Loader) maxRowErrors() int {
	if l.MaxRowErrors <= 0 {
		return 20
	}
	return l.MaxRowErrors
}

// copyBatch buffers up to chunkRows rows (or chunkBytes) and writes them in
// one COPY statement. A COPY is open only inside flush: a row refused while
// reading never leaves one open under the transaction's rollback.
type copyBatch struct {
	tx        *sql.Tx
	opts      Options
	chunkRows int
	rows      [][]any
	lines     []int
	bytes     int
}

// chunkBytes bounds the rows held in memory between two COPY statements.
const chunkBytes = 4 << 20

func (b *copyBatch) add(ctx context.Context, values []any, line int) error {
	b.rows = append(b.rows, values)
	b.lines = append(b.lines, line)
	for _, v := range values {
		if s, ok := v.(string); ok {
			b.bytes += len(s)
		}
	}
	if len(b.rows) >= b.chunkRows || b.bytes >= chunkBytes {
		return b.flush(ctx)
	}
	return nil
}

func (b *copyBatch) flush(ctx context.Context) error {
	if len(b.rows) == 0 {
		return nil
	}
	defer func() { b.rows, b.lines, b.bytes = b.rows[:0], b.lines[:0], 0 }()
	stmt, err := b.tx.PrepareContext(ctx, pq.CopyInSchema(b.opts.Schema, b.opts.Table, b.opts.ColumnNames()...))
	if err != nil {
		return classify(err, nil)
	}
	for _, values := range b.rows {
		if _, err := stmt.ExecContext(ctx, values...); err != nil {
			_ = stmt.Close()
			return classify(err, b.lines)
		}
	}
	if _, err := stmt.ExecContext(ctx); err != nil {
		_ = stmt.Close()
		return classify(err, b.lines)
	}
	if err := stmt.Close(); err != nil {
		return classify(err, b.lines)
	}
	return nil
}

var copyLinePattern = regexp.MustCompile(`COPY [^,]+, line (\d+)`)

// classify turns a Postgres error into what the user can act on. chunkLines
// maps a COPY error's line (within its chunk) back to the file's line.
func classify(err error, chunkLines []int) error {
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) {
		return err
	}
	switch pqErr.Code {
	case "42P07":
		return ErrTableExists
	case "42P01":
		return ErrTableMissing
	case "53100", "53000":
		return ErrDiskFull
	case "57014":
		return ErrTimedOut
	case "3F000":
		return &FileError{Msg: "the schema does not exist"}
	case "42501":
		return &FileError{Msg: "the database refused: " + pqErr.Message}
	}
	// Class 22 is a bad value, class 23 a broken constraint: both are a row.
	if code := string(pqErr.Code); strings.HasPrefix(code, "22") || strings.HasPrefix(code, "23") {
		return &RowErrors{Errors: []RowError{{Line: fileLine(pqErr.Where, chunkLines), Message: pqErr.Message}}}
	}
	return fmt.Errorf("the database refused the import: %s", pqErr.Message)
}

func fileLine(where string, chunkLines []int) int {
	match := copyLinePattern.FindStringSubmatch(where)
	if match == nil {
		return 0
	}
	n, err := strconv.Atoi(match[1])
	if err != nil || n < 1 || n > len(chunkLines) {
		return 0
	}
	return chunkLines[n-1]
}
