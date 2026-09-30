package tableimport

import (
	"errors"
	"strings"
	"testing"
)

func csvSource(t *testing.T, body string) Source {
	t.Helper()
	src, err := NewCSVSource(strings.NewReader(body), ',', testLimits())
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func TestPreview_InfersTypesAndSanitisesTheHeader(t *testing.T) {
	result, err := Preview(csvSource(t, "Full Name,Age,Joined\nann,31,2026-01-02\nbob,,2026-03-04\n"), PreviewOptions{HasHeader: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Columns) != 3 {
		t.Fatalf("columns = %+v", result.Columns)
	}
	want := []PreviewColumn{
		{Source: 0, SourceName: "Full Name", Name: "full_name", Type: TypeText},
		{Source: 1, SourceName: "Age", Name: "age", Type: TypeInteger},
		{Source: 2, SourceName: "Joined", Name: "joined", Type: TypeDate},
	}
	for i, w := range want {
		if result.Columns[i] != w {
			t.Errorf("column %d = %+v, want %+v", i, result.Columns[i], w)
		}
	}
	if len(result.Rows) != 2 || result.Rows[1][0] != "bob" {
		t.Fatalf("rows = %q", result.Rows)
	}
}

func TestPreview_WithoutAHeaderTheFirstRowIsData(t *testing.T) {
	result, err := Preview(csvSource(t, "1,x\n2,y\n"), PreviewOptions{HasHeader: false})
	if err != nil {
		t.Fatal(err)
	}
	if result.Columns[0].Name != "column_1" || result.Columns[0].Type != TypeInteger || len(result.Rows) != 2 {
		t.Fatalf("result = %+v", result)
	}
}

func TestPreview_ShowsAtMostThePreviewRows(t *testing.T) {
	body := "n\n" + strings.Repeat("1\n", 500)
	result, err := Preview(csvSource(t, body), PreviewOptions{HasHeader: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != PreviewRows || result.SampledRows != 500 {
		t.Fatalf("rows=%d sampled=%d", len(result.Rows), result.SampledRows)
	}
}

func TestPreview_AnEmptyFileIsRefused(t *testing.T) {
	if _, err := Preview(csvSource(t, ""), PreviewOptions{HasHeader: true}); !errors.Is(err, ErrEmptyFile) {
		t.Fatalf("err = %v", err)
	}
}

func TestPreview_AFileErrorInTheSampleIsReported(t *testing.T) {
	_, err := Preview(csvSource(t, "a\n\xff\n"), PreviewOptions{HasHeader: true})
	var fe *FileError
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v", err)
	}
}

func TestConvertRecord(t *testing.T) {
	opts := validOptions()
	opts.NullTokens = []string{"", "NULL"}
	conv := newConverter(opts, 3)

	values, rowErr := conv.convert([]string{"ann", "ignored", "31"}, 2)
	if rowErr != nil || values[0] != "ann" || values[1] != "31" {
		t.Fatalf("values=%v err=%v", values, rowErr)
	}
	values, rowErr = conv.convert([]string{"NULL", "", ""}, 3)
	if rowErr != nil || values[0] != nil || values[1] != nil {
		t.Fatalf("null tokens: values=%v err=%v", values, rowErr)
	}
	values, rowErr = conv.convert([]string{"short"}, 4)
	if rowErr != nil || values[1] != nil {
		t.Fatalf("short row: values=%v err=%v", values, rowErr)
	}
	_, rowErr = conv.convert([]string{"ann", "x", "thirty"}, 5)
	if rowErr == nil || rowErr.Line != 5 || rowErr.Column != "age" {
		t.Fatalf("bad integer: %+v", rowErr)
	}
	_, rowErr = conv.convert([]string{"a", "b", "1", "extra"}, 6)
	if rowErr == nil || rowErr.Line != 6 {
		t.Fatalf("extra cell: %+v", rowErr)
	}
	values, rowErr = conv.convert([]string{"a", "b", "1", ""}, 7)
	if rowErr != nil || len(values) != 2 {
		t.Fatalf("trailing empty cell: %v %+v", values, rowErr)
	}
}

func TestRowError_TruncatesTheEchoedValue(t *testing.T) {
	conv := newConverter(validOptions(), 3)
	_, rowErr := conv.convert([]string{"a", "", strings.Repeat("9", 500)}, 2)
	if rowErr == nil || len(rowErr.Value) > 80 {
		t.Fatalf("rowErr = %+v", rowErr)
	}
}

func TestCheckDiskHeadroom(t *testing.T) {
	const gi = int64(1) << 30
	if err := CheckDiskHeadroom(1*gi, 5*gi, 100<<20); err != nil {
		t.Fatalf("a small import on a roomy disk was refused: %v", err)
	}
	if err := CheckDiskHeadroom(4*gi, 5*gi, 200<<20); !errors.Is(err, ErrDiskFull) {
		t.Fatalf("err = %v", err)
	}
	if err := CheckDiskHeadroom(1*gi, 0, 100<<20); err != nil {
		t.Fatalf("an unknown disk size must not refuse: %v", err)
	}
}
