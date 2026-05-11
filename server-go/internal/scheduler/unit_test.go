package scheduler

import (
	"strings"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

// Pure-function tests that don't require Docker. These pad coverage for
// the helpers and document expected boundary behaviour.

func TestBackoffDelay_Curve(t *testing.T) {
	cases := []struct {
		attempt int
		min     time.Duration
		max     time.Duration
	}{
		{0, 0, 0},
		{1, time.Second, time.Second},
		{2, 2 * time.Second, 2 * time.Second},
		{3, 4 * time.Second, 4 * time.Second},
		{4, 8 * time.Second, 8 * time.Second},
		{5, 16 * time.Second, 16 * time.Second},
		// caps at 5 minutes
		{20, 300 * time.Second, 300 * time.Second},
	}
	for _, c := range cases {
		got := backoffDelay(c.attempt)
		if got < c.min || got > c.max {
			t.Errorf("backoffDelay(%d): got %v, want in [%v, %v]", c.attempt, got, c.min, c.max)
		}
	}
}

func TestAsTextArray_HandlesEmptyAndQuotes(t *testing.T) {
	if got := asTextArray(nil).(string); got != "{}" {
		t.Errorf("asTextArray(nil): got %q, want %q", got, "{}")
	}
	if got := asTextArray([]string{"abc"}).(string); got != `{"abc"}` {
		t.Errorf(`asTextArray(["abc"]): got %q, want %q`, got, `{"abc"}`)
	}
	// Embedded quote/backslash are escaped (defence-in-depth — task ids
	// never contain these, but we want the function safe regardless).
	got := asTextArray([]string{`a"b`, `c\d`}).(string)
	if !strings.Contains(got, `\"`) || !strings.Contains(got, `\\`) {
		t.Errorf(`asTextArray quote-escape failed: %s`, got)
	}
}

func TestNextDailyUTC_CrossesMidnight(t *testing.T) {
	// 2024-01-01 10:00 UTC, target 09:00 -> next day.
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	next := nextDailyUTC(now, 9, 0)
	if next.Day() != 2 || next.Hour() != 9 || next.Minute() != 0 {
		t.Errorf("nextDailyUTC: got %v, want 2024-01-02 09:00", next)
	}
}

func TestNextDailyUTC_LaterToday(t *testing.T) {
	now := time.Date(2024, 1, 1, 7, 0, 0, 0, time.UTC)
	next := nextDailyUTC(now, 9, 30)
	if next.Day() != 1 || next.Hour() != 9 || next.Minute() != 30 {
		t.Errorf("nextDailyUTC: got %v, want 2024-01-01 09:30", next)
	}
}

func TestNextHourlyUTC_CrossesHour(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 30, 0, 0, time.UTC)
	next := nextHourlyUTC(now, 15)
	if next.Hour() != 11 || next.Minute() != 15 {
		t.Errorf("nextHourlyUTC: got %v, want 11:15", next)
	}
}

func TestNextHourlyUTC_LaterInHour(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 5, 0, 0, time.UTC)
	next := nextHourlyUTC(now, 30)
	if next.Hour() != 10 || next.Minute() != 30 {
		t.Errorf("nextHourlyUTC: got %v, want 10:30", next)
	}
}

func TestNextDueAt_UnknownKindReturnsFalse(t *testing.T) {
	parser := cron.NewParser(
		cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow,
	)
	type sched = struct {
		Kind       string `json:"kind"`
		Expression string `json:"expression,omitempty"`
		Hours      int    `json:"hours,omitempty"`
		Minutes    int    `json:"minutes,omitempty"`
		Seconds    int    `json:"seconds,omitempty"`
		HourUTC    int    `json:"hourUTC,omitempty"`
		MinuteUTC  int    `json:"minuteUTC,omitempty"`
	}
	_, ok := nextDueAt(sched{Kind: "weekly", HourUTC: 9, MinuteUTC: 0}, time.Now(), parser)
	if ok {
		t.Errorf("nextDueAt: weekly kind should return ok=false")
	}
}

func TestNextDueAt_IntervalZeroIsInvalid(t *testing.T) {
	parser := cron.NewParser(
		cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow,
	)
	type sched = struct {
		Kind       string `json:"kind"`
		Expression string `json:"expression,omitempty"`
		Hours      int    `json:"hours,omitempty"`
		Minutes    int    `json:"minutes,omitempty"`
		Seconds    int    `json:"seconds,omitempty"`
		HourUTC    int    `json:"hourUTC,omitempty"`
		MinuteUTC  int    `json:"minuteUTC,omitempty"`
	}
	_, ok := nextDueAt(sched{Kind: "interval"}, time.Now(), parser)
	if ok {
		t.Errorf("nextDueAt: interval with zero duration should return ok=false")
	}
}

func TestNextDueAt_CronBadExpr(t *testing.T) {
	parser := cron.NewParser(
		cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow,
	)
	type sched = struct {
		Kind       string `json:"kind"`
		Expression string `json:"expression,omitempty"`
		Hours      int    `json:"hours,omitempty"`
		Minutes    int    `json:"minutes,omitempty"`
		Seconds    int    `json:"seconds,omitempty"`
		HourUTC    int    `json:"hourUTC,omitempty"`
		MinuteUTC  int    `json:"minuteUTC,omitempty"`
	}
	_, ok := nextDueAt(sched{Kind: "cron", Expression: "not a cron"}, time.Now(), parser)
	if ok {
		t.Errorf("nextDueAt: malformed cron expression should return ok=false")
	}
}

func TestBase32RandID_Shape(t *testing.T) {
	id := base32RandID()
	if len(id) != 30 {
		t.Errorf("base32RandID length: got %d, want 30", len(id))
	}
	for i, c := range id {
		if !strings.ContainsRune(base32Alphabet, c) {
			t.Errorf("base32RandID[%d]=%q not in alphabet", i, c)
		}
	}
}

func TestNewWorker_AppliesDefaults(t *testing.T) {
	w := NewWorker(WorkerConfig{})
	if w.poll <= 0 {
		t.Errorf("default poll interval: got %v, want >0", w.poll)
	}
	if w.batch <= 0 {
		t.Errorf("default batch: got %d, want >0", w.batch)
	}
	if w.maxAttempts <= 0 {
		t.Errorf("default maxAttempts: got %d, want >0", w.maxAttempts)
	}
}

func TestNewCronRunner_AppliesDefaults(t *testing.T) {
	cr := NewCronRunner(CronRunnerConfig{})
	if cr.idGen == nil {
		t.Errorf("default idGen: got nil")
	}
	if cr.logger == nil {
		t.Errorf("default logger: got nil")
	}
}
