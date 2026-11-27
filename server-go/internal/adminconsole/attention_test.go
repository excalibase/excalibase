package adminconsole

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeAttention struct {
	got   AttentionQuery
	items []SuspendedBrief
	err   error
}

func (f *fakeAttention) ListSuspended(_ context.Context, q AttentionQuery) (CursorPage[SuspendedBrief], error) {
	f.got = q
	return CursorPage[SuspendedBrief]{Items: f.items, Total: len(f.items), NextCursor: "next"}, f.err
}

func (f *fakeAttention) ListFailedProjects(_ context.Context, q AttentionQuery) (CursorPage[ProjectBrief], error) {
	f.got = q
	return CursorPage[ProjectBrief]{Items: []ProjectBrief{}}, f.err
}

func TestCursorRoundTripsATimeAndAnID(t *testing.T) {
	at := time.Date(2026, 11, 27, 10, 30, 0, 123456000, time.UTC)
	decoded, err := DecodeCursor(EncodeCursor(Cursor{At: &at, ID: "u-9"}))
	if err != nil || decoded.ID != "u-9" || decoded.At == nil || !decoded.At.Equal(at) {
		t.Fatalf("decoded = %+v, %v", decoded, err)
	}
	none, err := DecodeCursor(EncodeCursor(Cursor{ID: "p-1"}))
	if err != nil || none.At != nil || none.ID != "p-1" {
		t.Fatalf("a row without a time must round-trip as without one: %+v, %v", none, err)
	}
}

func TestADamagedCursorIsRefusedNotGuessed(t *testing.T) {
	for _, bad := range []string{"!!", "bm90LWEtY3Vyc29y", "MXw", "YWJj"} {
		if _, err := DecodeCursor(bad); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("DecodeCursor(%q) = %v, want ErrInvalidQuery", bad, err)
		}
	}
}

func TestSuspensionsPageSizeIsDefaultedAndCapped(t *testing.T) {
	cases := []struct{ asked, want int }{{0, DefaultAttentionPage}, {-3, DefaultAttentionPage}, {20, 20}, {50, 50}, {51, MaxAttentionPage}, {9999, MaxAttentionPage}}
	for _, tc := range cases {
		reader := &fakeAttention{}
		if _, err := NewAttention(reader).Suspensions(context.Background(), "", tc.asked); err != nil {
			t.Fatal(err)
		}
		if reader.got.Limit != tc.want {
			t.Errorf("asked %d: reader limit = %d, want %d", tc.asked, reader.got.Limit, tc.want)
		}
	}
	if MaxAttentionPage != 50 || DefaultAttentionPage != 20 {
		t.Fatalf("page sizes are 20 by default and 50 at most, got %d and %d", DefaultAttentionPage, MaxAttentionPage)
	}
}

func TestSuspensionsPassTheDecodedCursorAndTheDayWindow(t *testing.T) {
	at := time.Date(2026, 11, 27, 1, 0, 0, 0, time.UTC)
	reader := &fakeAttention{}
	attention := NewAttention(reader)
	attention.now = func() time.Time { return time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC) }
	if _, err := attention.Suspensions(context.Background(), EncodeCursor(Cursor{At: &at, ID: "u1"}), 10); err != nil {
		t.Fatal(err)
	}
	if reader.got.After == nil || reader.got.After.ID != "u1" || !reader.got.Since.Equal(time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("query = %+v", reader.got)
	}
}

func TestABadCursorNeverReachesTheDatabase(t *testing.T) {
	reader := &fakeAttention{}
	if _, err := NewAttention(reader).Suspensions(context.Background(), "garbage!", 10); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("err = %v, want ErrInvalidQuery", err)
	}
	if reader.got.Limit != 0 {
		t.Fatal("the reader was asked despite a bad cursor")
	}
}

func TestAReadFailureIsWrappedNotSwallowed(t *testing.T) {
	boom := errors.New("boom")
	if _, err := NewAttention(&fakeAttention{err: boom}).FailedProjects(context.Background(), "", 5); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}
