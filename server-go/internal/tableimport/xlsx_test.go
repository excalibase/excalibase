package tableimport

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func writeWorkbook(t *testing.T, build func(f *excelize.File)) string {
	t.Helper()
	f := excelize.NewFile()
	build(f)
	path := filepath.Join(t.TempDir(), "book.xlsx")
	if err := f.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestXLSX_ReadsTheFirstSheetByDefault(t *testing.T) {
	path := writeWorkbook(t, func(f *excelize.File) {
		_ = f.SetSheetRow("Sheet1", "A1", &[]any{"name", "qty"})
		_ = f.SetSheetRow("Sheet1", "A2", &[]any{"ann", 3})
		_, _ = f.NewSheet("Other")
		_ = f.SetSheetRow("Other", "A1", &[]any{"x"})
	})
	wb, err := OpenXLSX(path, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer wb.Close()
	if got := strings.Join(wb.Sheets(), ","); got != "Sheet1,Other" {
		t.Fatalf("sheets = %s", got)
	}
	src, err := wb.Source("")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := readAll(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1][0] != "ann" || rows[1][1] != "3" {
		t.Fatalf("rows = %q", rows)
	}
	other, err := wb.Source("Other")
	if err != nil {
		t.Fatal(err)
	}
	if rows, _ := readAll(t, other); len(rows) != 1 || rows[0][0] != "x" {
		t.Fatalf("other = %q", rows)
	}
	if _, err := wb.Source("Missing"); err == nil {
		t.Fatal("a missing sheet was accepted")
	}
}

// A formula is never evaluated: its cell holds what the file cached, and a
// formula with no cached value imports as empty, not as its result.
func TestXLSX_FormulasAreNeverEvaluated(t *testing.T) {
	path := writeWorkbook(t, func(f *excelize.File) {
		_ = f.SetCellValue("Sheet1", "A1", "v")
		_ = f.SetCellFormula("Sheet1", "A2", "1+1")
		_ = f.SetCellValue("Sheet1", "A3", "=cmd|' /C calc'!A0")
	})
	wb, err := OpenXLSX(path, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer wb.Close()
	src, _ := wb.Source("")
	rows, err := readAll(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 3 {
		t.Fatalf("rows = %q", rows)
	}
	if len(rows[1]) > 0 && rows[1][0] == "2" {
		t.Fatal("the formula was evaluated")
	}
	if rows[2][0] != "=cmd|' /C calc'!A0" {
		t.Fatalf("formula text changed: %q", rows[2][0])
	}
}

func TestXLSX_TooManySheetsIsRefused(t *testing.T) {
	path := writeWorkbook(t, func(f *excelize.File) {
		for i := 0; i < 6; i++ {
			_, _ = f.NewSheet(fmt.Sprintf("S%d", i))
		}
	})
	if _, err := OpenXLSX(path, testLimits()); err == nil || !strings.Contains(err.Error(), "sheets") {
		t.Fatalf("err = %v", err)
	}
}

func TestXLSX_TooManyRowsIsRefused(t *testing.T) {
	lim := testLimits()
	lim.MaxRows = 10
	path := writeWorkbook(t, func(f *excelize.File) {
		for i := 1; i <= 12; i++ {
			_ = f.SetCellValue("Sheet1", fmt.Sprintf("A%d", i), i)
		}
	})
	wb, err := OpenXLSX(path, lim)
	if err != nil {
		t.Fatal(err)
	}
	defer wb.Close()
	src, _ := wb.Source("")
	if _, err := readAll(t, src); !errors.Is(err, ErrTooManyRows) {
		t.Fatalf("err = %v", err)
	}
}

// A row far down the sheet is not a way around the row cap.
func TestXLSX_ASparseFarRowCountsAgainstTheCap(t *testing.T) {
	lim := testLimits()
	path := writeWorkbook(t, func(f *excelize.File) {
		_ = f.SetCellValue("Sheet1", "A1", "a")
		_ = f.SetCellValue("Sheet1", "A1048576", "z")
	})
	wb, err := OpenXLSX(path, lim)
	if err != nil {
		t.Fatal(err)
	}
	defer wb.Close()
	src, _ := wb.Source("")
	if _, err := readAll(t, src); !errors.Is(err, ErrTooManyRows) {
		t.Fatalf("err = %v", err)
	}
}

func TestXLSX_AFarColumnCountsAgainstTheColumnCap(t *testing.T) {
	path := writeWorkbook(t, func(f *excelize.File) {
		_ = f.SetCellValue("Sheet1", "A1", "a")
		_ = f.SetCellValue("Sheet1", "XFD1", "z")
	})
	wb, err := OpenXLSX(path, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer wb.Close()
	src, _ := wb.Source("")
	_, err = readAll(t, src)
	var fe *FileError
	if !errors.As(err, &fe) || !strings.Contains(fe.Error(), "columns") {
		t.Fatalf("err = %v", err)
	}
}

// rewriteZip copies a workbook and lets add append entries to it.
func rewriteZip(t *testing.T, src string, add func(w *zip.Writer)) string {
	t.Helper()
	r, err := zip.OpenReader(src)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, f := range r.File {
		rc, _ := f.Open()
		out, _ := w.Create(f.Name)
		_, _ = io.Copy(out, rc)
		rc.Close()
	}
	add(w)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "rewritten.xlsx")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func plainWorkbook(t *testing.T) string {
	return writeWorkbook(t, func(f *excelize.File) { _ = f.SetCellValue("Sheet1", "A1", "a") })
}

func TestXLSX_MacrosAreRefused(t *testing.T) {
	path := rewriteZip(t, plainWorkbook(t), func(w *zip.Writer) {
		out, _ := w.Create("xl/vbaProject.bin")
		_, _ = out.Write([]byte("macro"))
	})
	if _, err := OpenXLSX(path, testLimits()); !errors.Is(err, ErrActiveContent) {
		t.Fatalf("err = %v", err)
	}
}

func TestXLSX_ExternalLinksAreRefused(t *testing.T) {
	path := rewriteZip(t, plainWorkbook(t), func(w *zip.Writer) {
		out, _ := w.Create("xl/externalLinks/externalLink1.xml")
		_, _ = out.Write([]byte("<externalLink/>"))
	})
	if _, err := OpenXLSX(path, testLimits()); !errors.Is(err, ErrActiveContent) {
		t.Fatalf("err = %v", err)
	}
}

// A small file that inflates past the cap is refused from its declared sizes.
func TestXLSX_ZipBombIsRefused(t *testing.T) {
	lim := testLimits()
	path := rewriteZip(t, plainWorkbook(t), func(w *zip.Writer) {
		out, _ := w.Create("xl/worksheets/sheet2.xml")
		_, _ = io.CopyN(out, &endless{b: '0'}, lim.MaxUnzippedBytes+1)
	})
	if info, _ := os.Stat(path); info.Size() > lim.MaxXLSXBytes {
		t.Fatalf("bomb is %d bytes, not small", info.Size())
	}
	if _, err := OpenXLSX(path, lim); !errors.Is(err, ErrZipBomb) {
		t.Fatalf("err = %v", err)
	}
}

// A bomb whose header lies about its size is caught when it is inflated.
func TestXLSX_ZipBombWithALyingHeaderIsRefused(t *testing.T) {
	lim := testLimits()
	var deflated bytes.Buffer
	fw, _ := flate.NewWriter(&deflated, flate.BestCompression)
	crc := crc32.NewIEEE()
	_, _ = io.CopyN(io.MultiWriter(fw, crc), &endless{b: '0'}, lim.MaxUnzippedBytes*2)
	_ = fw.Close()
	path := rewriteZip(t, plainWorkbook(t), func(w *zip.Writer) {
		out, _ := w.CreateRaw(&zip.FileHeader{
			Name: "xl/sharedStrings2.xml", Method: zip.Deflate, CRC32: crc.Sum32(),
			CompressedSize64: uint64(deflated.Len()), UncompressedSize64: 10,
		})
		_, _ = out.Write(deflated.Bytes())
	})
	wb, err := OpenXLSX(path, lim)
	if err == nil {
		defer wb.Close()
		src, serr := wb.Source("")
		if serr == nil {
			_, err = readAll(t, src)
		} else {
			err = serr
		}
	}
	if err == nil {
		t.Fatal("a lying zip bomb was read without error")
	}
}

func TestXLSX_AFileThatIsNotAWorkbookIsRefused(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	out, _ := w.Create("hello.txt")
	_, _ = out.Write([]byte("hi"))
	_ = w.Close()
	path := filepath.Join(t.TempDir(), "x.xlsx")
	_ = os.WriteFile(path, buf.Bytes(), 0o600)
	if _, err := OpenXLSX(path, testLimits()); err == nil {
		t.Fatal("a zip without a workbook was accepted")
	}
}

func TestXLSX_TheFileCapAppliesToWorkbooks(t *testing.T) {
	lim := testLimits()
	lim.MaxXLSXBytes = 100
	if _, err := OpenXLSX(plainWorkbook(t), lim); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("err = %v", err)
	}
}
