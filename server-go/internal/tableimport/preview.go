package tableimport

import (
	"errors"
	"fmt"
	"io"
)

const (
	// PreviewRows is how many rows the preview shows.
	PreviewRows = 50
	// sampleRows is how many rows type inference looks at.
	sampleRows = 1000
	maxEchoed  = 80
)

// PreviewOptions shape how the first rows are read.
type PreviewOptions struct {
	HasHeader  bool
	NullTokens []string
}

// PreviewColumn is a suggested mapping for one column of the file.
type PreviewColumn struct {
	Source     int        `json:"source"`
	SourceName string     `json:"sourceName"`
	Name       string     `json:"name"`
	Type       ColumnType `json:"type"`
}

// PreviewResult is what Studio shows before anything is written.
type PreviewResult struct {
	Columns     []PreviewColumn `json:"columns"`
	Rows        [][]string      `json:"rows"`
	SampledRows int             `json:"sampledRows"`
}

// Preview reads up to sampleRows data rows and suggests names and types.
func Preview(src Source, opts PreviewOptions) (PreviewResult, error) {
	var header []string
	var sample [][]string
	width := 0
	for len(sample) < sampleRows {
		record, _, err := src.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return PreviewResult{}, err
		}
		if opts.HasHeader && header == nil {
			header = record
			width = max(width, len(record))
			continue
		}
		sample = append(sample, record)
		width = max(width, len(record))
	}
	if width == 0 {
		return PreviewResult{}, ErrEmptyFile
	}
	return buildPreview(header, sample, width, opts), nil
}

func buildPreview(header []string, sample [][]string, width int, opts PreviewOptions) PreviewResult {
	padded := make([]string, width)
	copy(padded, header)
	names := ColumnNames(padded, opts.HasHeader)
	nulls := opts.NullTokens
	if len(nulls) == 0 {
		nulls = []string{""}
	}
	types := InferTypes(sample, width, nulls)
	columns := make([]PreviewColumn, width)
	for i := range columns {
		columns[i] = PreviewColumn{Source: i, SourceName: padded[i], Name: names[i], Type: types[i]}
	}
	shown := sample
	if len(shown) > PreviewRows {
		shown = shown[:PreviewRows]
	}
	return PreviewResult{Columns: columns, Rows: shown, SampledRows: len(sample)}
}

// converter turns a record into COPY values for the chosen columns.
type converter struct {
	columns []ColumnSpec
	nulls   map[string]bool
	width   int
}

// newConverter checks records against width, the column count of the
// header (or first row).
func newConverter(opts Options, width int) converter {
	return converter{columns: opts.Columns, nulls: tokenSet(opts.Nulls()), width: width}
}

func (c converter) convert(record []string, line int) ([]any, *RowError) {
	if len(trimTrailingEmpty(record)) > c.width {
		return nil, &RowError{Line: line, Message: fmt.Sprintf("the row has %d cells; the first row has %d", len(record), c.width)}
	}
	values := make([]any, len(c.columns))
	for i, col := range c.columns {
		if col.Source >= len(record) || c.nulls[record[col.Source]] {
			values[i] = nil
			continue
		}
		value := record[col.Source]
		if err := CheckValue(col.Type, value); err != nil {
			return nil, &RowError{Line: line, Column: col.Name, Value: echo(value), Message: fmt.Sprintf("%s %s", col.Type, err)}
		}
		values[i] = value
	}
	return values, nil
}

func echo(value string) string {
	if len(value) <= maxEchoed {
		return value
	}
	cut := maxEchoed - len("…")
	for cut > 0 && value[cut]&0xC0 == 0x80 {
		cut--
	}
	return value[:cut] + "…"
}

// Postgres needs room for the table, its WAL and indexes: about three times
// the file, kept under 90% of the disk.
const (
	importAmplification = 3
	diskFullPercent     = 90
)

// CheckDiskHeadroom refuses an import that would take the disk past 90%.
// A disk of unknown size is left to Postgres, which fails the load cleanly.
func CheckDiskHeadroom(usedBytes, diskBytes, importBytes int64) error {
	if diskBytes <= 0 {
		return nil
	}
	if usedBytes+importAmplification*importBytes > diskBytes/100*diskFullPercent {
		free := max(diskBytes-usedBytes, 0)
		return fmt.Errorf("%w: about %d MiB is free and this import needs about %d MiB; grow the disk or import a smaller file",
			ErrDiskFull, free>>20, (importAmplification*importBytes)>>20)
	}
	return nil
}
