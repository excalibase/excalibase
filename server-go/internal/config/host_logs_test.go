package config

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestParseHostLogsDefaults(t *testing.T) {
	got, err := ParseHostLogs(" http://loki:3100/ ", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.LokiURL != "http://loki:3100" || got.DailyBytesPerProject != 64<<20 {
		t.Errorf("host logs = %+v", got)
	}
	if got, err := ParseHostLogs("", ""); err != nil || got.LokiURL != "" {
		t.Errorf("unset = %+v %v", got, err)
	}
}

func TestParseHostLogsDailyBudget(t *testing.T) {
	got, err := ParseHostLogs("http://loki:3100", "16")
	if err != nil || got.DailyBytesPerProject != 16<<20 {
		t.Errorf("16 MiB = %+v %v", got, err)
	}
	for _, bad := range []string{"0", "-1", "abc", "1.5", "102401"} {
		if _, err := ParseHostLogs("http://loki:3100", bad); err == nil {
			t.Errorf("HOST_LOGS_DAILY_MB=%q accepted", bad)
		}
	}
}

func TestParseHostLogsRefusesAnAddressThatIsNotPlainHTTP(t *testing.T) {
	for _, bad := range []string{"loki:3100", "ftp://loki", "http://", "http://user:pw@loki:3100", "http://loki:3100?x=1", "http://loki:3100/#f"} {
		if _, err := ParseHostLogs(bad, ""); err == nil {
			t.Errorf("HOST_LOGS_LOKI_URL=%q accepted", bad)
		}
	}
}

// EXC-678: each plan's daily budget, in LOG_RETENTION_BY_PLAN's shape.
func TestParseHostLogBudgetsByPlan(t *testing.T) {
	got, err := ParseHostLogBudgets("free=64,standard=256,enterprise=1024")
	if err != nil || got[domain.Free] != 64<<20 || got[domain.Standard] != 256<<20 || got[domain.Enterprise] != 1<<30 {
		t.Errorf("budgets = %v %v", got, err)
	}
	if got, err := ParseHostLogBudgets(""); err != nil || len(got) != 0 {
		t.Errorf("unset = %v %v", got, err)
	}
	for _, bad := range []string{"free", "free=0", "free=x", "pro=64", "FREE=64", "free=64,free=32", "free=102401", " free=64"} {
		if _, err := ParseHostLogBudgets(bad); err == nil {
			t.Errorf("HOST_LOGS_DAILY_MB_BY_PLAN=%q accepted", bad)
		}
	}
}

func TestParseHostLogMinFreePercent(t *testing.T) {
	if got, err := ParseHostLogMinFreePercent(""); err != nil || got != 10 {
		t.Errorf("default = %d %v", got, err)
	}
	if got, err := ParseHostLogMinFreePercent("20"); err != nil || got != 20 {
		t.Errorf("20 = %d %v", got, err)
	}
	for _, bad := range []string{"0", "51", "x", "-5", "10%"} {
		if _, err := ParseHostLogMinFreePercent(bad); err == nil {
			t.Errorf("HOST_LOGS_MIN_FREE_PERCENT=%q accepted", bad)
		}
	}
}

func TestLoadHostLogsReadsTheCaps(t *testing.T) {
	t.Setenv("HOST_LOGS_DAILY_MB_BY_PLAN", "free=8")
	t.Setenv("HOST_LOGS_MIN_FREE_PERCENT", "15")
	t.Setenv("HOST_LOGS_DISK_PATH", "/data")
	got := loadHostLogs()
	if got.DailyBytesByPlan[domain.Free] != 8<<20 || got.MinFreePercent != 15 || got.DiskPath != "/data" {
		t.Errorf("host logs = %+v", got)
	}
	t.Setenv("HOST_LOGS_DISK_PATH", "")
	if got := loadHostLogs(); got.DiskPath != defaultHostLogDiskPath {
		t.Errorf("default disk path = %q", got.DiskPath)
	}
}
