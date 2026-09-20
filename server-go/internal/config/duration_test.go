package config

import (
	"strings"
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

func TestLoadReadsThePauseTimeout(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.excalibase.io")
	t.Setenv("EXCALIBASE_PAUSE_TIMEOUT", "25m")

	if got := Load().PauseTimeout; got != 25*time.Minute {
		t.Errorf("PauseTimeout: got %v, want 25m", got)
	}
}

func TestParsePlatformDBMaxConnsAcceptsAUsableSize(t *testing.T) {
	got, err := parsePlatformDBMaxConns("40")
	if err != nil {
		t.Fatalf("parsePlatformDBMaxConns: %v", err)
	}
	if got != 40 {
		t.Errorf("got %d, want 40", got)
	}
}

func TestParsePlatformDBMaxConnsFallsBackOnlyWhenUnset(t *testing.T) {
	got, err := parsePlatformDBMaxConns("  ")
	if err != nil {
		t.Fatalf("parsePlatformDBMaxConns: %v", err)
	}
	if got != DefaultPlatformDBMaxConns {
		t.Errorf("got %d, want the default %d", got, DefaultPlatformDBMaxConns)
	}
}

// A pool too small to hold the standing leadership claims plus one operation
// plus one query cannot work, so it is refused rather than silently replaced
// — the operator would otherwise never learn their setting was ignored.
func TestParsePlatformDBMaxConnsRefusesUnusableSizes(t *testing.T) {
	for _, raw := range []string{"ten", "0", "-5", "1", "5", "6"} {
		if _, err := parsePlatformDBMaxConns(raw); err == nil {
			t.Errorf("%q must be refused, not silently replaced by the default", raw)
		}
	}
	_, err := parsePlatformDBMaxConns("3")
	if err == nil || !strings.Contains(err.Error(), "7") {
		t.Errorf("the error must state the minimum, got %v", err)
	}
	if !strings.Contains(err.Error(), "5 standing") {
		t.Errorf("the error must explain the standing claims, got %v", err)
	}
}

// The function scheduler's cron half leads on the platform database too, so
// it is a fifth standing claim and the minimum pool has to account for it.
func TestMinPlatformDBMaxConnsCountsTheCronLeadershipClaim(t *testing.T) {
	const standingClaims = 5 // backup, idle pause, storage reap, restore sweep, function cron
	if MinPlatformDBMaxConns != standingClaims+2 {
		t.Errorf("minimum pool: got %d, want %d (%d standing claims + one operation + one query)",
			MinPlatformDBMaxConns, standingClaims+2, standingClaims)
	}
}

func TestLoadReadsThePlatformDBMaxConns(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.excalibase.io")
	t.Setenv("PLATFORM_DB_MAX_CONNS", "33")

	if got := Load().PlatformDBMaxConns; got != 33 {
		t.Errorf("PlatformDBMaxConns: got %d, want 33", got)
	}
}
