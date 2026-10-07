package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func TestUTCTimeMarshalsWithItsZone(t *testing.T) {
	saigon := time.FixedZone("ICT", 7*3600)
	b, err := json.Marshal(UTCTime{Time: time.Date(2026, 10, 7, 9, 30, 0, 5, saigon)})
	if err != nil || string(b) != `"2026-10-07T02:30:00.000000005Z"` {
		t.Fatalf("got %s %v", b, err)
	}
}

func TestUTCTimeReadsZonedAndOlderZonelessTimes(t *testing.T) {
	for raw, want := range map[string]time.Time{
		`"2026-10-07T02:30:00Z"`:          time.Date(2026, 10, 7, 2, 30, 0, 0, time.UTC),
		`"2026-10-07T09:30:00+07:00"`:     time.Date(2026, 10, 7, 2, 30, 0, 0, time.UTC),
		`"2026-10-07T02:30:00.000000000"`: time.Date(2026, 10, 7, 2, 30, 0, 0, time.UTC),
		`"2026-10-07T02:30:00"`:           time.Date(2026, 10, 7, 2, 30, 0, 0, time.UTC),
	} {
		var got UTCTime
		if err := json.Unmarshal([]byte(raw), &got); err != nil || !got.Time.Equal(want) {
			t.Errorf("%s: got %v %v", raw, got.Time, err)
		}
	}
	var empty UTCTime
	if err := json.Unmarshal([]byte(`""`), &empty); err != nil || !empty.Time.IsZero() {
		t.Errorf("empty: %v %v", empty.Time, err)
	}
	var bad UTCTime
	if err := json.Unmarshal([]byte(`"yesterday"`), &bad); err == nil {
		t.Error("an unreadable time must be refused")
	}
}

func TestMigrationRecordAppliedAtCarriesItsZone(t *testing.T) {
	record := MigrationRecord{ID: "m1", AppliedAt: &UTCTime{Time: time.Date(2026, 10, 7, 2, 30, 0, 0, time.UTC)}}
	b, _ := json.Marshal(record)
	var shaped struct {
		AppliedAt string `json:"appliedAt"`
	}
	if err := json.Unmarshal(b, &shaped); err != nil || shaped.AppliedAt != "2026-10-07T02:30:00Z" {
		t.Fatalf("appliedAt = %q (%v)", shaped.AppliedAt, err)
	}
}
