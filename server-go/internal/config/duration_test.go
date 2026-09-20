package config

import (
	"testing"
	"time"
)

func TestParseDurationAcceptsGoDurations(t *testing.T) {
	got, err := parseDuration("90s", time.Minute)
	if err != nil {
		t.Fatalf("parseDuration: %v", err)
	}
	if got != 90*time.Second {
		t.Errorf("got %v, want 90s", got)
	}
}

func TestParseDurationFallsBackOnlyWhenUnset(t *testing.T) {
	got, err := parseDuration("   ", 7*time.Minute)
	if err != nil {
		t.Fatalf("parseDuration: %v", err)
	}
	if got != 7*time.Minute {
		t.Errorf("got %v, want the fallback", got)
	}
}

func TestParseDurationRefusesUnreadableValues(t *testing.T) {
	for _, raw := range []string{"fifteen minutes", "15", "-5m", "0"} {
		if _, err := parseDuration(raw, time.Minute); err == nil {
			t.Errorf("%q must be refused, not silently replaced by the default", raw)
		}
	}
}

func TestLoadReadsTheRestoreReadyTimeout(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.excalibase.io")
	t.Setenv("EXCALIBASE_RESTORE_READY_TIMEOUT", "42m")

	if got := Load().RestoreReadyTimeout; got != 42*time.Minute {
		t.Errorf("RestoreReadyTimeout: got %v, want 42m", got)
	}
}
