package config

import (
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// Owner decision (ADR 0041, 2026-10-11): point-in-time within the backup
// window, hourly on Standard, every 6 hours on Free.
func TestMySQLBackupSchedule(t *testing.T) {
	for tier, want := range map[domain.TierType]string{
		domain.Free: "0 */6 * * *", domain.Standard: "0 * * * *", domain.Enterprise: "0 * * * *",
	} {
		got, err := MySQLBackupSchedule(tier)
		if err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", tier, got, err, want)
		}
	}
	if _, err := MySQLBackupSchedule("GOLD"); err == nil {
		t.Error("an unknown tier got a schedule")
	}
}

func TestMySQLBackupIntervalMatchesTheSchedule(t *testing.T) {
	for tier, want := range map[domain.TierType]time.Duration{domain.Free: 6 * time.Hour, domain.Standard: time.Hour, domain.Enterprise: time.Hour} {
		if got, err := MySQLBackupInterval(tier); err != nil || got != want {
			t.Errorf("%s: %v %v", tier, got, err)
		}
	}
	if _, err := MySQLBackupInterval("GOLD"); err == nil {
		t.Error("a tier without a schedule got an interval")
	}
}
