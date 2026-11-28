package adminconsole

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeAuditReader struct {
	got  AuditQuery
	page AuditPage
	err  error
}

func (f *fakeAuditReader) ListAudit(_ context.Context, q AuditQuery) (AuditPage, error) {
	f.got = q
	return f.page, f.err
}

func TestAuditPageSizeIsDefaultedAndCapped(t *testing.T) {
	cases := []struct{ asked, want int }{{0, DefaultAuditPage}, {-1, DefaultAuditPage}, {10, 10}, {MaxAuditPage, MaxAuditPage}, {MaxAuditPage + 1, MaxAuditPage}}
	for _, tc := range cases {
		reader := &fakeAuditReader{}
		if _, err := NewAuditLog(reader).List(context.Background(), AuditFilter{}, "", tc.asked); err != nil {
			t.Fatal(err)
		}
		if reader.got.Limit != tc.want {
			t.Errorf("asked %d: limit = %d, want %d", tc.asked, reader.got.Limit, tc.want)
		}
	}
}

func TestAuditCursorMustBeOneWeMade(t *testing.T) {
	for _, bad := range []string{"!!", EncodeCursor(Cursor{ID: "not-a-number"}), EncodeCursor(Cursor{ID: "-4"})} {
		reader := &fakeAuditReader{}
		if _, err := NewAuditLog(reader).List(context.Background(), AuditFilter{}, bad, 5); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("cursor %q: err = %v, want ErrInvalidQuery", bad, err)
		}
	}
	reader := &fakeAuditReader{}
	if _, err := NewAuditLog(reader).List(context.Background(), AuditFilter{}, EncodeCursor(Cursor{ID: "42"}), 5); err != nil {
		t.Fatal(err)
	}
	if reader.got.BeforeID != 42 {
		t.Fatalf("BeforeID = %d, want 42", reader.got.BeforeID)
	}
}

func TestAuditFiltersAreTrimmedAndBounded(t *testing.T) {
	reader := &fakeAuditReader{}
	filter := AuditFilter{Actor: "  boss@x.test ", Action: " user.", Target: " u-1 "}
	if _, err := NewAuditLog(reader).List(context.Background(), filter, "", 5); err != nil {
		t.Fatal(err)
	}
	if reader.got.Actor != "boss@x.test" || reader.got.Action != "user." || reader.got.Target != "u-1" {
		t.Fatalf("filters not trimmed: %+v", reader.got)
	}
	long := strings.Repeat("a", MaxAuditFilterLength+1)
	for _, bad := range []AuditFilter{{Actor: long}, {Action: long}, {Target: long}} {
		if _, err := NewAuditLog(&fakeAuditReader{}).List(context.Background(), bad, "", 5); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("filter %+v: err = %v, want ErrInvalidQuery", bad, err)
		}
	}
}

func TestAuditDetailsAreCutShortAndTheCursorPointsPastTheLastRow(t *testing.T) {
	reader := &fakeAuditReader{page: AuditPage{Items: []AuditRow{{ID: "9", Details: strings.Repeat("x", MaxAuditDetails+50)}, {ID: "7"}}, More: true}}
	page, err := NewAuditLog(reader).List(context.Background(), AuditFilter{}, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := len([]rune(page.Items[0].Details)); got > MaxAuditDetails+1 {
		t.Fatalf("details length = %d, want at most %d", got, MaxAuditDetails+1)
	}
	next, err := DecodeCursor(page.NextCursor)
	if err != nil || next.ID != "7" {
		t.Fatalf("next cursor = %+v, %v; want the last row's id 7", next, err)
	}
	reader.page.More = false
	page, _ = NewAuditLog(reader).List(context.Background(), AuditFilter{}, "", 2)
	if page.NextCursor != "" {
		t.Fatalf("the last page must have no cursor, got %q", page.NextCursor)
	}
}

func TestAuditReadFailureIsWrapped(t *testing.T) {
	boom := errors.New("db down")
	if _, err := NewAuditLog(&fakeAuditReader{err: boom}).List(context.Background(), AuditFilter{}, "", 5); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the reader's", err)
	}
}
