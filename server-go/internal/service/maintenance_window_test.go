package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestNormalizeMaintenanceWindowAcceptsADayAndTime(t *testing.T) {
	cases := map[string]string{
		"sunday 02:00":   "sunday 02:00",
		"Saturday 23:59": "saturday 23:59",
		" mon 00:00 ":    "monday 00:00",
		"THU 7:05":       "thursday 07:05",
	}
	for window, want := range cases {
		got, err := normalizeMaintenanceWindow(domain.MaintenanceWindowConfig{Window: window, DurationMinutes: 60})
		if err != nil || got.Window != want || got.DurationMinutes != 60 {
			t.Errorf("%q: got %+v, %v; want window %q", window, got, err, want)
		}
	}
	cleared, err := normalizeMaintenanceWindow(domain.MaintenanceWindowConfig{})
	if err != nil || cleared.Window != "" || cleared.DurationMinutes != 0 {
		t.Errorf("an empty window clears it: got %+v, %v", cleared, err)
	}
}

func TestNormalizeMaintenanceWindowRefusesWhatCannotBeAWindow(t *testing.T) {
	for name, cfg := range map[string]domain.MaintenanceWindowConfig{
		"no day":             {Window: "02:00", DurationMinutes: 60},
		"unknown day":        {Window: "someday 02:00", DurationMinutes: 60},
		"hour out of range":  {Window: "sunday 24:00", DurationMinutes: 60},
		"minute range":       {Window: "sunday 02:60", DurationMinutes: 60},
		"free text":          {Window: "whenever", DurationMinutes: 60},
		"no duration":        {Window: "sunday 02:00"},
		"negative duration":  {Window: "sunday 02:00", DurationMinutes: -30},
		"over a day":         {Window: "sunday 02:00", DurationMinutes: MaxMaintenanceWindowMinutes + 1},
		"duration no window": {DurationMinutes: 60},
	} {
		if _, err := normalizeMaintenanceWindow(cfg); !errors.Is(err, ErrInvalidMaintenanceWindow) {
			t.Errorf("%s: err = %v, want ErrInvalidMaintenanceWindow", name, err)
		}
	}
}

// A refused window changes nothing: it is checked before the project is held.
func TestSetMaintenanceWindowRefusesAnInvalidWindow(t *testing.T) {
	svc, _, _ := setupOpsTest(t)
	err := svc.SetMaintenanceWindow(context.Background(), testOpsDB, domain.MaintenanceWindowConfig{Window: "sunday 25:00", DurationMinutes: 60})
	if !errors.Is(err, ErrInvalidMaintenanceWindow) {
		t.Fatalf("err = %v, want ErrInvalidMaintenanceWindow", err)
	}
}

func TestSetMaintenanceWindowRecordsTheCanonicalWindow(t *testing.T) {
	svc, _, _ := setupOpsTest(t)
	if err := svc.SetMaintenanceWindow(context.Background(), testOpsDB, domain.MaintenanceWindowConfig{Window: "Sun 2:00", DurationMinutes: 90}); err != nil {
		t.Fatalf("SetMaintenanceWindow: %v", err)
	}
	got, err := svc.GetMaintenanceWindow(testOpsDB)
	if err != nil || got.Window != "sunday 02:00" || got.DurationMinutes != 90 {
		t.Errorf("got %+v, %v; want sunday 02:00 for 90 minutes", got, err)
	}
}
