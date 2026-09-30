package tableimport

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/xuri/excelize/v2"
)

// A deflated entry that expands past this ratio is treated as a bomb even
// under the total cap; real sheets compress around 10:1.
const maxCompressionRatio = 250

const maxZipEntries = 5000

// Entries that make a workbook more than data. Excelize would ignore most of
// them, but a file carrying them is refused rather than half-read.
var activeContent = []string{"vbaproject", "externallinks/", "activex/", "embeddings/", "macrosheets/", "customui/"}

// Workbook is an opened, pre-checked XLSX file.
type Workbook struct {
	file   *excelize.File
	sheets []string
	lim    Limits
}

// OpenXLSX checks the zip container before any XML is parsed: size, entry
// count, declared expansion and active content. Excelize then reads it with
// the same expansion cap, spilling large sheets to TmpDir.
func OpenXLSX(path string, lim Limits) (*Workbook, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > lim.MaxXLSXBytes {
		return nil, ErrFileTooLarge
	}
	if err := precheckZip(path, lim); err != nil {
		return nil, err
	}
	file, err := excelize.OpenFile(path, excelize.Options{
		UnzipSizeLimit:    lim.MaxUnzippedBytes,
		UnzipXMLSizeLimit: min(16*mib, lim.MaxUnzippedBytes),
		TmpDir:            os.TempDir(),
	})
	if err != nil {
		return nil, &FileError{Msg: "the workbook could not be read: " + sanitiseLibError(err)}
	}
	sheets := file.GetSheetList()
	if len(sheets) == 0 {
		_ = file.Close()
		return nil, &FileError{Msg: "the workbook has no sheets"}
	}
	if len(sheets) > lim.MaxSheets {
		_ = file.Close()
		return nil, &FileError{Msg: fmt.Sprintf("the workbook has %d sheets; an import allows %d", len(sheets), lim.MaxSheets)}
	}
	return &Workbook{file: file, sheets: sheets, lim: lim}, nil
}

func precheckZip(path string, lim Limits) error {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return &FileError{Msg: "the file is not a valid XLSX workbook"}
	}
	defer archive.Close()
	if len(archive.File) > maxZipEntries {
		return ErrZipBomb
	}
	var total uint64
	hasWorkbook := false
	for _, entry := range archive.File {
		name := strings.ToLower(entry.Name)
		for _, marker := range activeContent {
			if strings.Contains(name, marker) {
				return ErrActiveContent
			}
		}
		if name == "xl/workbook.xml" {
			hasWorkbook = true
		}
		total += entry.UncompressedSize64
		if total > uint64(lim.MaxUnzippedBytes) {
			return ErrZipBomb
		}
		if entry.CompressedSize64 > 0 && entry.UncompressedSize64/entry.CompressedSize64 > maxCompressionRatio {
			return ErrZipBomb
		}
	}
	if !hasWorkbook {
		return &FileError{Msg: "the file is a zip archive but not an XLSX workbook"}
	}
	return verifyEntries(archive.File)
}

// verifyEntries inflates every entry once, to nowhere. archive/zip refuses
// an entry that inflates past its declared size or fails its checksum, so a
// header that lies about its size is caught here, bounded by the total cap
// the declared sizes already passed.
func verifyEntries(entries []*zip.File) error {
	for _, entry := range entries {
		rc, err := entry.Open()
		if err != nil {
			return ErrZipBomb
		}
		_, err = io.Copy(io.Discard, io.LimitReader(rc, int64(entry.UncompressedSize64)+1))
		_ = rc.Close()
		if err != nil {
			return ErrZipBomb
		}
	}
	return nil
}

// sanitiseLibError keeps a library message but never a server path.
func sanitiseLibError(err error) string {
	msg := err.Error()
	if strings.Contains(msg, os.TempDir()) || strings.Contains(msg, "/") {
		return "the file is damaged or not a workbook"
	}
	return msg
}

// Sheets lists the workbook's sheets in order.
func (w *Workbook) Sheets() []string { return w.sheets }

// Close releases the workbook and its spill files.
func (w *Workbook) Close() error { return w.file.Close() }

// Source reads one sheet; "" is the first.
func (w *Workbook) Source(sheet string) (RecordReader, error) {
	if sheet == "" {
		sheet = w.sheets[0]
	}
	found := false
	for _, name := range w.sheets {
		found = found || name == sheet
	}
	if !found {
		return nil, &FileError{Msg: fmt.Sprintf("the workbook has no sheet named %q", sheet)}
	}
	rows, err := w.file.Rows(sheet)
	if err != nil {
		return nil, &FileError{Msg: "the sheet could not be read: " + sanitiseLibError(err)}
	}
	return &xlsxSource{rows: rows, lim: w.lim}, nil
}

type xlsxSource struct {
	rows *excelize.Rows
	lim  Limits
	line int
}

// Next yields the sheet's rows with trailing empty cells dropped. The value
// is the cell's cached, formatted value: formulas are never calculated.
func (s *xlsxSource) Next() ([]string, int, error) {
	if !s.rows.Next() {
		if err := s.rows.Error(); err != nil {
			return nil, s.line, &FileError{Line: s.line, Msg: "the sheet could not be read: " + sanitiseLibError(err)}
		}
		_ = s.rows.Close()
		return nil, s.line, io.EOF
	}
	s.line++
	if s.line > s.lim.MaxRows+1 {
		return nil, s.line, ErrTooManyRows
	}
	record, err := s.rows.Columns()
	if err != nil {
		if errors.Is(err, excelize.ErrColumnNumber) {
			return nil, s.line, &FileError{Line: s.line, Msg: "the row has more columns than an import allows"}
		}
		return nil, s.line, &FileError{Line: s.line, Msg: "the row could not be read: " + sanitiseLibError(err)}
	}
	record = trimTrailingEmpty(record)
	if err := checkRecord(record, s.line, s.lim); err != nil {
		return nil, s.line, err
	}
	return record, s.line, nil
}

func trimTrailingEmpty(record []string) []string {
	end := len(record)
	for end > 0 && record[end-1] == "" {
		end--
	}
	return record[:end]
}
