// Package tableimport turns a CSV/TSV file, an XLSX workbook or a published
// Google Sheet into a new or existing table (EXC-368). Every byte is parsed
// here, server-side, under hard caps; nothing the client declares about the
// file is believed, and no cell is ever evaluated.
package tableimport

import (
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// Limits bound what one import may cost the control plane and the tenant.
type Limits struct {
	MaxBytes         int64 `json:"maxBytes"`
	MaxXLSXBytes     int64 `json:"maxXlsxBytes"`
	MaxUnzippedBytes int64 `json:"-"`
	MaxRows          int   `json:"maxRows"`
	MaxColumns       int   `json:"maxColumns"`
	MaxCellBytes     int   `json:"-"`
	MaxRecordBytes   int   `json:"-"`
	MaxSheets        int   `json:"-"`
}

const mib = int64(1) << 20

// Workbooks are capped below text files on every plan: the XLSX reader holds
// shared strings in memory, and the control plane runs in 512 MiB.
func baseLimits(maxBytes int64, maxRows int) Limits {
	return Limits{
		MaxBytes:         maxBytes,
		MaxXLSXBytes:     20 * mib,
		MaxUnzippedBytes: 100 * mib,
		MaxRows:          maxRows,
		MaxColumns:       500,
		MaxCellBytes:     256 << 10,
		MaxRecordBytes:   2 << 20,
		MaxSheets:        50,
	}
}

var tierLimits = map[domain.TierType]Limits{
	domain.Free:       baseLimits(50*mib, 1_000_000),
	domain.Standard:   baseLimits(200*mib, 5_000_000),
	domain.Enterprise: baseLimits(500*mib, 20_000_000),
}

// ForTier is the import allowance of a plan.
func ForTier(tier domain.TierType) (Limits, error) {
	lim, ok := tierLimits[tier]
	if !ok {
		return Limits{}, fmt.Errorf("no import limits for plan %q", tier)
	}
	return lim, nil
}

var (
	ErrFileTooLarge      = errors.New("the file is larger than your plan allows for an import")
	ErrRecordTooLarge    = errors.New("a row of the file is larger than an import allows")
	ErrTooManyRows       = errors.New("the file has more rows than your plan allows for an import")
	ErrEmptyFile         = errors.New("the file is empty")
	ErrUnsupportedFormat = errors.New("the file is not a CSV, TSV or XLSX file")
	ErrNotUTF8           = errors.New("the file is not UTF-8 text; save it as CSV UTF-8 (in Excel: Save As, CSV UTF-8) and try again")
	ErrActiveContent     = errors.New("the workbook contains macros, external links or embedded objects; save it as a plain .xlsx or .csv")
	ErrZipBomb           = errors.New("the workbook expands to more data than an import allows")
	ErrTableExists       = errors.New("a table with that name already exists")
	ErrTableMissing      = errors.New("the table to append to does not exist")
	ErrColumnsMissing    = errors.New("the table has no column named")
	ErrDiskFull          = errors.New("the database disk does not have room for this import")
	ErrTimedOut          = errors.New("the import ran past the platform's statement timeout; split the file into smaller parts")
)

// FileError is a structural problem with the file at a given line.
type FileError struct {
	Line int
	Msg  string
}

func (e *FileError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("line %d: %s", e.Line, e.Msg)
	}
	return e.Msg
}

// RowError is one row whose value does not fit its column.
type RowError struct {
	Line    int    `json:"line"`
	Column  string `json:"column,omitempty"`
	Value   string `json:"value,omitempty"`
	Message string `json:"message"`
}

// RowErrors is a refused load: nothing was written.
type RowErrors struct {
	Errors []RowError
}

func (e *RowErrors) Error() string {
	if len(e.Errors) == 0 {
		return "the import was refused"
	}
	first := e.Errors[0]
	return fmt.Sprintf("%d row(s) could not be imported; first at line %d: %s", len(e.Errors), first.Line, first.Message)
}
