package tableimport

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// RecordReader yields a file's records in order with the line each began on;
// io.EOF ends it.
type RecordReader interface {
	Next() (record []string, line int, err error)
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// capReader fails once more than max bytes have been read in total, and
// once a single record has pulled more than recordMax from the file. The
// record count is reset by the CSV source before each record, so a file with
// no line ends cannot make the parser buffer it whole.
type capReader struct {
	r                 io.Reader
	total, max        int64
	record, recordMax int64
}

func (c *capReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.total += int64(n)
	c.record += int64(n)
	if c.total > c.max {
		return n, ErrFileTooLarge
	}
	if c.recordMax > 0 && c.record > c.recordMax {
		return n, ErrRecordTooLarge
	}
	return n, err
}

type csvRecords struct {
	reader *csv.Reader
	limit  *capReader
	lim    Limits
	rows   int
}

// NewCSVSource parses delimited text under lim. The file cap is enforced as
// the body streams, so an oversized upload fails without being stored.
func NewCSVSource(r io.Reader, delimiter rune, lim Limits) (RecordReader, error) {
	// The record allowance covers bufio's read-ahead on top of the record.
	limited := &capReader{r: r, max: lim.MaxBytes, recordMax: int64(lim.MaxRecordBytes) + 64<<10}
	buffered := bufio.NewReader(limited)
	if head, err := buffered.Peek(len(utf8BOM)); err == nil && bytes.Equal(head, utf8BOM) {
		_, _ = buffered.Discard(len(utf8BOM))
	}
	reader := csv.NewReader(buffered)
	reader.Comma = delimiter
	reader.FieldsPerRecord = -1
	return &csvRecords{reader: reader, limit: limited, lim: lim}, nil
}

func (s *csvRecords) Next() ([]string, int, error) {
	s.limit.record = 0
	record, err := s.reader.Read()
	if err != nil {
		return nil, 0, csvError(err)
	}
	line, _ := s.reader.FieldPos(0)
	s.rows++
	if s.rows > s.lim.MaxRows+1 {
		return nil, line, ErrTooManyRows
	}
	if err := checkRecord(record, line, s.lim); err != nil {
		return nil, line, err
	}
	return record, line, nil
}

func csvError(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, ErrFileTooLarge) || errors.Is(err, ErrRecordTooLarge) {
		return err
	}
	var parseErr *csv.ParseError
	if errors.As(err, &parseErr) {
		if errors.Is(parseErr.Err, ErrFileTooLarge) || errors.Is(parseErr.Err, ErrRecordTooLarge) {
			return parseErr.Err
		}
		return &FileError{Line: parseErr.StartLine, Msg: parseErr.Err.Error()}
	}
	return err
}

// checkRecord applies the per-record caps every source shares.
func checkRecord(record []string, line int, lim Limits) error {
	if len(record) > lim.MaxColumns {
		return &FileError{Line: line, Msg: fmt.Sprintf("the row has %d columns; an import allows %d", len(record), lim.MaxColumns)}
	}
	for _, cell := range record {
		if len(cell) > lim.MaxCellBytes {
			return &FileError{Line: line, Msg: fmt.Sprintf("a cell is larger than %d KiB", lim.MaxCellBytes>>10)}
		}
		if !utf8.ValidString(cell) {
			return &FileError{Line: line, Msg: "the text is not valid UTF-8; save the file as UTF-8 and try again"}
		}
		if strings.IndexByte(cell, 0) >= 0 {
			return &FileError{Line: line, Msg: "a cell contains a NUL byte, which Postgres text cannot hold"}
		}
	}
	return nil
}

var sniffCandidates = []rune{',', '\t', ';', '|'}

// SniffDelimiter picks the separator that appears most often, outside
// quotes, on the first line; a comma when nothing stands out.
func SniffDelimiter(head []byte) rune {
	head = bytes.TrimPrefix(head, utf8BOM)
	counts := make(map[rune]int, len(sniffCandidates))
	inQuotes := false
	for _, b := range string(head) {
		if b == '"' {
			inQuotes = !inQuotes
			continue
		}
		if inQuotes {
			continue
		}
		if b == '\n' {
			break
		}
		counts[b]++
	}
	best, bestCount := ',', 0
	for _, candidate := range sniffCandidates {
		if counts[candidate] > bestCount {
			best, bestCount = candidate, counts[candidate]
		}
	}
	return best
}

// ParseDelimiter accepts a known separator, or "tab".
func ParseDelimiter(raw string) (rune, error) {
	if raw == "tab" {
		return '\t', nil
	}
	for _, candidate := range sniffCandidates {
		if raw == string(candidate) {
			return candidate, nil
		}
	}
	return 0, fmt.Errorf("delimiter must be one of , ; | or tab")
}
