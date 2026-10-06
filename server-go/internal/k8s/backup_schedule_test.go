package k8s

import (
	"errors"
	"strings"
	"testing"
)

// A backup schedule is the standard five-field crontab on every route, and
// runs at most once an hour.
func TestValidateBackupScheduleAcceptsFiveFieldsAtMostHourly(t *testing.T) {
	for _, schedule := range []string{"0 2 * * *", "30 */6 * * 1-5", "0 0 1 * *", "15 * * * *", "@hourly", "@daily", "@weekly", "@monthly"} {
		if err := ValidateBackupSchedule(schedule); err != nil {
			t.Errorf("%q: %v", schedule, err)
		}
	}
}

func TestValidateBackupScheduleRefusesOtherShapes(t *testing.T) {
	cases := map[string]string{
		"":              "five",
		"0 0 2 * * *":   "five",
		"0 2 * *":       "five",
		"0 25 * * *":    "five",
		"every day":     "five",
		"@every 1h":     "five",
		"* * * * *":     "once an hour",
		"*/15 * * * *":  "once an hour",
		"0,30 * * * *":  "once an hour",
		"0-5 2 * * *":   "once an hour",
		"@annually":     "five",
		"0 2 * * * # x": "five",
	}
	for schedule, want := range cases {
		err := ValidateBackupSchedule(schedule)
		if !errors.Is(err, ErrInvalidBackupSchedule) {
			t.Errorf("%q: err = %v, want ErrInvalidBackupSchedule", schedule, err)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %q should mention %q", schedule, err, want)
		}
	}
}

// CloudNativePG reads a leading seconds field, so the five fields are given
// to it with seconds pinned to zero.
func TestCNPGBackupScheduleAddsTheSecondsField(t *testing.T) {
	cases := map[string]string{
		"0 2 * * *":      "0 0 2 * * *",
		"30 */6 * * 1-5": "0 30 */6 * * 1-5",
		"@hourly":        "0 0 * * * *",
		"@daily":         "0 0 0 * * *",
		"@weekly":        "0 0 0 * * 0",
		"@monthly":       "0 0 0 1 * *",
	}
	for schedule, want := range cases {
		got, err := CNPGBackupSchedule(schedule)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", schedule, got, err, want)
		}
	}
	if _, err := CNPGBackupSchedule("* * * * *"); !errors.Is(err, ErrInvalidBackupSchedule) {
		t.Errorf("an every-minute schedule must be refused, got %v", err)
	}
}

func TestBuildScheduledBackupWritesTheSixFieldForm(t *testing.T) {
	obj, err := BuildScheduledBackup("p", "ns", "0 3 * * *")
	if err != nil {
		t.Fatal(err)
	}
	spec := obj.Object["spec"].(map[string]interface{})
	if spec["schedule"] != "0 0 3 * * *" {
		t.Errorf("schedule = %v, want the six-field form", spec["schedule"])
	}
}

func TestBuildScheduledBackupRefusesAnInvalidSchedule(t *testing.T) {
	obj, err := BuildScheduledBackup("p", "ns", "0 0 2 * * *")
	if !errors.Is(err, ErrInvalidBackupSchedule) || obj != nil {
		t.Fatalf("got %v, %v; want no object and ErrInvalidBackupSchedule", obj, err)
	}
}
