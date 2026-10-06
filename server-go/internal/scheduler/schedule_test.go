package scheduler

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidateScheduleAcceptsWhatTheRunnerRuns(t *testing.T) {
	for _, raw := range []string{
		`{"kind":"cron","expression":"0 0 * * *"}`,
		`{"kind":"cron","expression":"*/5 9-17 * * 1-5"}`,
		`{"kind":"cron","expression":"@hourly"}`,
		`{"kind":"cron","expression":"@daily"}`,
		`{"kind":"cron","expression":"@weekly"}`,
		`{"kind":"cron","expression":"@monthly"}`,
		`{"kind":"interval","hours":1}`,
		`{"kind":"interval","minutes":5,"seconds":30}`,
		`{"kind":"daily","hourUTC":23,"minuteUTC":59}`,
		`{"kind":"daily","hourUTC":0,"minuteUTC":0}`,
		`{"kind":"hourly","minuteUTC":15}`,
	} {
		if err := ValidateSchedule(json.RawMessage(raw)); err != nil {
			t.Errorf("ValidateSchedule(%s) = %v, want nil", raw, err)
		}
	}
}

func TestValidateScheduleRefusesWhatWouldNeverRunAsWritten(t *testing.T) {
	cases := map[string]string{
		`{"kind":"cron","expression":""}`:             "expression",
		`{"kind":"cron","expression":"every day"}`:    "cron",
		`{"kind":"cron","expression":"0 25 * * *"}`:   "cron",
		`{"kind":"cron","expression":"61 * * * *"}`:   "cron",
		`{"kind":"cron","expression":"0 0 0 * * *"}`:  "five fields",
		`{"kind":"cron","expression":"@every 1m"}`:    "interval",
		`{"kind":"cron","expression":"@yearly"}`:      "@hourly",
		`{"kind":"interval"}`:                         "interval",
		`{"kind":"interval","hours":-1,"minutes":90}`: "negative",
		`{"kind":"interval","minutes":1.5}`:           "whole",
		`{"kind":"daily","hourUTC":25,"minuteUTC":0}`: "hourUTC",
		`{"kind":"daily","hourUTC":3,"minuteUTC":60}`: "minuteUTC",
		`{"kind":"daily","hourUTC":-1}`:               "hourUTC",
		`{"kind":"hourly","minuteUTC":75}`:            "minuteUTC",
		`{"kind":"weekly"}`:                           "kind",
		`{}`:                                          "kind",
		`[]`:                                          "object",
		`{"kind":"interval","hours":"1"}`:             "numeric",
		``:                                            "schedule",
	}
	for raw, want := range cases {
		err := ValidateSchedule(json.RawMessage(raw))
		if !errors.Is(err, ErrInvalidSchedule) {
			t.Errorf("ValidateSchedule(%s) = %v, want ErrInvalidSchedule", raw, err)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ValidateSchedule(%s) = %q, want it to mention %q", raw, err, want)
		}
	}
}

func TestDescriptorSchedulesRunOnTheRunner(t *testing.T) {
	r := NewCronRunner(CronRunnerConfig{})
	now := time.Date(2026, 1, 1, 10, 30, 0, 0, time.UTC)
	cases := map[string]time.Time{
		"@hourly":  time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC),
		"@daily":   time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		"@weekly":  time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC),
		"@monthly": time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
	}
	for expr, want := range cases {
		var s struct {
			Kind       string `json:"kind"`
			Expression string `json:"expression,omitempty"`
			Hours      int    `json:"hours,omitempty"`
			Minutes    int    `json:"minutes,omitempty"`
			Seconds    int    `json:"seconds,omitempty"`
			HourUTC    int    `json:"hourUTC,omitempty"`
			MinuteUTC  int    `json:"minuteUTC,omitempty"`
		}
		s.Kind, s.Expression = "cron", expr
		got, ok := nextDueAt(s, now, r.parser)
		if !ok || !got.Equal(want) {
			t.Errorf("%s: next = %v (%v), want %v", expr, got, ok, want)
		}
	}
}
