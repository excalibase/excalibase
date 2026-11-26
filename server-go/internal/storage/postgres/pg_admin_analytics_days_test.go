package postgres

import (
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
)

func TestFillDaysListsEveryDayOfTheWindowOldestFirst(t *testing.T) {
	now := time.Date(2026, 11, 27, 15, 4, 0, 0, time.UTC)
	start := windowStart(now, 3)
	got := fillDays(start, 3, map[string]adminconsole.DayCount{"2026-11-26": {Day: "2026-11-26", Total: 5, Flagged: 2}})
	want := []adminconsole.DayCount{{Day: "2026-11-25"}, {Day: "2026-11-26", Total: 5, Flagged: 2}, {Day: "2026-11-27"}}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("day %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if len(got) != 3 {
		t.Fatalf("len = %d", len(got))
	}
}
