package tableimport

import (
	"errors"
	"testing"

	"github.com/lib/pq"
)

func TestClassify(t *testing.T) {
	cases := map[pq.ErrorCode]error{
		"42P07": ErrTableExists, "42P01": ErrTableMissing, "53100": ErrDiskFull, "57014": ErrTimedOut,
	}
	for code, want := range cases {
		if got := classify(&pq.Error{Code: code}, nil); !errors.Is(got, want) {
			t.Errorf("%s -> %v", code, got)
		}
	}
	var rowErrs *RowErrors
	got := classify(&pq.Error{Code: "22007", Message: "bad date", Where: "COPY t, line 2, column d: \"x\""}, []int{10, 11, 12})
	if !errors.As(got, &rowErrs) || rowErrs.Errors[0].Line != 11 {
		t.Fatalf("got %v", got)
	}
	var fileErr *FileError
	if !errors.As(classify(&pq.Error{Code: "3F000"}, nil), &fileErr) {
		t.Error("missing schema is not a file error")
	}
	if got := classify(&pq.Error{Code: "XX000", Message: "boom"}, nil); got == nil {
		t.Error("an internal error vanished")
	}
	plain := errors.New("net")
	if classify(plain, nil) != plain {
		t.Error("a non-Postgres error was rewritten")
	}
	if fileLine("no copy here", []int{1}) != 0 || fileLine("COPY t, line 9, column", []int{1}) != 0 {
		t.Error("fileLine invented a line")
	}
}
