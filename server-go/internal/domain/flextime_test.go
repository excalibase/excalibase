package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func TestFlexTimeMarshal(t *testing.T) {
	ft := &FlexTime{Time: time.Date(2026, 3, 26, 14, 30, 0, 0, time.UTC)}
	b, err := json.Marshal(ft)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `"2026-03-26T14:30:00.000000000"` {
		t.Errorf("got %s", string(b))
	}
}

func TestFlexTimeUnmarshalRFC3339(t *testing.T) {
	var ft FlexTime
	err := json.Unmarshal([]byte(`"2026-03-26T14:30:00Z"`), &ft)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ft.Time.Year() != 2026 || ft.Time.Month() != 3 || ft.Time.Day() != 26 {
		t.Errorf("wrong date: %v", ft.Time)
	}
}

func TestFlexTimeUnmarshalJavaFormat(t *testing.T) {
	var ft FlexTime
	err := json.Unmarshal([]byte(`"2026-03-08T15:02:44.835080881"`), &ft)
	if err != nil {
		t.Fatalf("unmarshal Java format: %v", err)
	}
	if ft.Time.Year() != 2026 || ft.Time.Hour() != 15 {
		t.Errorf("wrong time: %v", ft.Time)
	}
}

func TestFlexTimeUnmarshalSimple(t *testing.T) {
	var ft FlexTime
	err := json.Unmarshal([]byte(`"2026-03-08T15:02:44"`), &ft)
	if err != nil {
		t.Fatalf("unmarshal simple: %v", err)
	}
	if ft.Time.Second() != 44 {
		t.Errorf("wrong second: %v", ft.Time)
	}
}

func TestFlexTimeUnmarshalNull(t *testing.T) {
	var ft FlexTime
	err := json.Unmarshal([]byte(`""`), &ft)
	if err != nil {
		t.Fatalf("unmarshal empty: %v", err)
	}
	if !ft.Time.IsZero() {
		t.Error("should be zero for empty string")
	}
}

func TestFlexTimeRoundTrip(t *testing.T) {
	original := &FlexTime{Time: time.Now().UTC().Truncate(time.Second)}
	b, _ := json.Marshal(original)

	var decoded FlexTime
	json.Unmarshal(b, &decoded)

	if original.Time.Unix() != decoded.Time.Unix() {
		t.Errorf("round trip failed: %v vs %v", original.Time, decoded.Time)
	}
}

func TestFlexTimeInStruct(t *testing.T) {
	type S struct {
		Created *FlexTime `json:"created,omitempty"`
	}

	// Nil pointer → omitted
	b, _ := json.Marshal(S{})
	if string(b) != `{}` {
		t.Errorf("nil flextime: got %s", string(b))
	}

	// Non-nil → serialized
	now := &FlexTime{Time: time.Now()}
	b, _ = json.Marshal(S{Created: now})
	if string(b) == `{}` {
		t.Error("flextime should be serialized")
	}
}
