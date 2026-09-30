package tableimport

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func testLimits() Limits {
	return Limits{
		MaxBytes: 1 << 20, MaxXLSXBytes: 1 << 20, MaxUnzippedBytes: 8 << 20,
		MaxRows: 1000, MaxColumns: 20, MaxCellBytes: 1024, MaxRecordBytes: 8 << 10, MaxSheets: 5,
	}
}

func readAll(t *testing.T, src Source) ([][]string, error) {
	t.Helper()
	var out [][]string
	for {
		rec, _, err := src.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, rec)
	}
}

func TestCSV_ReadsRecordsAndStripsTheBOM(t *testing.T) {
	src, err := NewCSVSource(strings.NewReader("\ufeffname,age\nann,31\n\"b, c\",\"4\"\n"), ',', testLimits())
	if err != nil {
		t.Fatal(err)
	}
	rows, err := readAll(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0][0] != "name" || rows[2][0] != "b, c" {
		t.Fatalf("rows = %q", rows)
	}
}

func TestCSV_FormulaCellsAreKeptAsText(t *testing.T) {
	src, _ := NewCSVSource(strings.NewReader("f\n=cmd|' /C calc'!A0\n+1+1\n@SUM(A1)\n"), ',', testLimits())
	rows, err := readAll(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if rows[1][0] != "=cmd|' /C calc'!A0" || rows[2][0] != "+1+1" || rows[3][0] != "@SUM(A1)" {
		t.Fatalf("formula cells changed: %q", rows)
	}
}

func TestCSV_BadUTF8IsRefusedWithItsLine(t *testing.T) {
	src, _ := NewCSVSource(strings.NewReader("a\nok\nbad\xff\xfe\n"), ',', testLimits())
	_, err := readAll(t, src)
	var fe *FileError
	if !errors.As(err, &fe) || fe.Line != 3 || !strings.Contains(fe.Error(), "UTF-8") {
		t.Fatalf("err = %v", err)
	}
}

func TestCSV_NULBytesAreRefused(t *testing.T) {
	src, _ := NewCSVSource(strings.NewReader("a\nx\x00y\n"), ',', testLimits())
	_, err := readAll(t, src)
	var fe *FileError
	if !errors.As(err, &fe) || fe.Line != 2 {
		t.Fatalf("err = %v", err)
	}
}

func TestCSV_AHugeRecordIsRefusedBeforeItIsBuffered(t *testing.T) {
	lim := testLimits()
	lim.MaxCellBytes = 1 << 30
	huge := io.MultiReader(strings.NewReader("a\n\""), &endless{b: 'x'})
	src, _ := NewCSVSource(huge, ',', lim)
	_, err := readAll(t, src)
	if !errors.Is(err, ErrRecordTooLarge) && !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

func TestCSV_AHugeCellIsRefused(t *testing.T) {
	lim := testLimits()
	src, _ := NewCSVSource(strings.NewReader("a\n"+strings.Repeat("x", lim.MaxCellBytes+1)+"\n"), ',', lim)
	_, err := readAll(t, src)
	var fe *FileError
	if !errors.As(err, &fe) || !strings.Contains(fe.Error(), "cell") {
		t.Fatalf("err = %v", err)
	}
}

func TestCSV_TooManyColumnsIsRefused(t *testing.T) {
	lim := testLimits()
	src, _ := NewCSVSource(strings.NewReader(strings.Repeat("a,", lim.MaxColumns)+"a\n"), ',', lim)
	_, err := readAll(t, src)
	var fe *FileError
	if !errors.As(err, &fe) || !strings.Contains(fe.Error(), "columns") {
		t.Fatalf("err = %v", err)
	}
}

func TestCSV_TheFileCapIsEnforcedWhileStreaming(t *testing.T) {
	lim := testLimits()
	lim.MaxBytes = 100
	src, _ := NewCSVSource(strings.NewReader(strings.Repeat("abc\n", 100)), ',', lim)
	_, err := readAll(t, src)
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

func TestCSV_MalformedQuotingNamesTheLine(t *testing.T) {
	src, _ := NewCSVSource(strings.NewReader("a,b\n1,2\n3,x\"y\n"), ',', testLimits())
	_, err := readAll(t, src)
	var fe *FileError
	if !errors.As(err, &fe) || fe.Line != 3 {
		t.Fatalf("err = %v", err)
	}
}

func TestSniffDelimiter(t *testing.T) {
	cases := map[string]rune{
		"a,b,c\n1,2,3\n":       ',',
		"a\tb\tc\n1\t2\t3\n":   '\t',
		"a;b;c\n1;2;3\n":       ';',
		"\"x,y\";b;c\n1;2;3\n": ';',
		"single\n":             ',',
	}
	for input, want := range cases {
		if got := SniffDelimiter([]byte(input)); got != want {
			t.Errorf("SniffDelimiter(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestParseDelimiter_OnlyKnownSeparators(t *testing.T) {
	for _, ok := range []string{",", "\t", ";", "|", "tab"} {
		if _, err := ParseDelimiter(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"\"", "\n", "ab", "\r"} {
		if _, err := ParseDelimiter(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// endless yields the same byte forever: a file with no end of record.
type endless struct{ b byte }

func (e *endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = e.b
	}
	return len(p), nil
}
