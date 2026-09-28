package domain

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// A drill lost the last batch: 02:18:06.782Z was sent to Postgres as
// 02:18:06Z, and a commit at 02:18:06.7 is after that.
func TestRecoveryTargetKeepsTheSubSecondPart(t *testing.T) {
	at := time.Date(2026, 9, 28, 2, 18, 6, 782_345_678, time.UTC)
	got := RestoreRequest{TargetTime: &ZonedTime{Time: at}}.RecoveryTarget()["targetTime"]
	if got != "2026-09-28T02:18:06.782345Z" {
		t.Errorf("targetTime: got %v, want microsecond precision", got)
	}
}

func TestRecoveryTargetIsTheSameInstantWhateverZoneItWasSentIn(t *testing.T) {
	var r RestoreRequest
	if err := json.Unmarshal([]byte(`{"targetTime":"2026-09-28T09:18:06.782+07:00"}`), &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := r.RecoveryTarget()["targetTime"]; got != "2026-09-28T02:18:06.782000Z" {
		t.Errorf("targetTime: got %v, want the UTC instant", got)
	}
	if kind, value := r.RestoreTargetKind(); kind != "time" || value != "2026-09-28T02:18:06.782000Z" {
		t.Errorf("target kind: got (%q,%q)", kind, value)
	}
}

func TestTargetTimeMustCarryAZone(t *testing.T) {
	for _, body := range []string{
		`{"targetTime":"2026-09-28T02:18:06"}`,
		`{"targetTime":"2026-09-28T02:18:06.782"}`,
		`{"targetTime":"2026-09-28 02:18:06"}`,
		`{"targetTime":"yesterday"}`,
		`{"targetTime":""}`,
		`{"targetTime":12}`,
	} {
		var r RestoreRequest
		err := json.Unmarshal([]byte(body), &r)
		if !errors.Is(err, ErrTargetTimeFormat) {
			t.Errorf("%s: got %v, want ErrTargetTimeFormat", body, err)
		}
	}
}

func TestTargetTimeAbsentOrNullMeansNoTimeTarget(t *testing.T) {
	for _, body := range []string{`{}`, `{"targetTime":null}`} {
		var r RestoreRequest
		if err := json.Unmarshal([]byte(body), &r); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if r.TargetTime != nil {
			t.Errorf("%s: got a time target %v", body, r.TargetTime)
		}
	}
}
