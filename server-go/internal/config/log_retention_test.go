package config

import (
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
)

// EXC-659: the chart passes logs.retention and logs.retentionByPlan as these.
func TestParseLogRetention_ReadsThePlatformPeriodAndEachPlan(t *testing.T) {
	got, err := ParseLogRetention("72h", "enterprise=720h,free=24h,standard=168h")
	if err != nil {
		t.Fatal(err)
	}
	if got.Default != 72*time.Hour {
		t.Errorf("default %s", got.Default)
	}
	want := map[domain.TierType]time.Duration{domain.Free: 24 * time.Hour, domain.Standard: 168 * time.Hour, domain.Enterprise: 720 * time.Hour}
	for tier, period := range want {
		if got.ByPlan[tier] != period {
			t.Errorf("%s: %s, want %s", tier, got.ByPlan[tier], period)
		}
	}
}

func TestParseLogRetention_UnsetIsSevenDaysForEveryone(t *testing.T) {
	got, err := ParseLogRetention("", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Default != 7*24*time.Hour || len(got.ByPlan) != 0 {
		t.Errorf("unset: %+v", got)
	}
	if got.For(domain.Free) != 7*24*time.Hour {
		t.Errorf("free unset: %s", got.For(domain.Free))
	}
}

func TestLogRetention_APlanNotNamedKeepsThePlatformPeriod(t *testing.T) {
	got, _ := ParseLogRetention("168h", "free=24h")
	if got.For(domain.Free) != 24*time.Hour || got.For(domain.Standard) != 168*time.Hour {
		t.Errorf("free %s standard %s", got.For(domain.Free), got.For(domain.Standard))
	}
}

func TestParseLogRetention_RefusesWhatLokiCannotKeep(t *testing.T) {
	for _, tc := range []struct{ platform, byPlan string }{
		{"7d", ""},
		{"12h", ""},
		{"168h", "free=12h"},
		{"168h", "free=1d"},
		{"168h", "pro=24h"},
		{"168h", "free"},
		{"168h", "free=24h,free=48h"},
		{"168h", "FREE=24h"},
	} {
		if _, err := ParseLogRetention(tc.platform, tc.byPlan); err == nil {
			t.Errorf("LOG_RETENTION=%q LOG_RETENTION_BY_PLAN=%q accepted", tc.platform, tc.byPlan)
		}
	}
}

func TestDarkFeatures_LogRetentionVariable(t *testing.T) {
	if got := featureEnv[features.LogRetention]; got != "FEATURE_LOGRETENTION" {
		t.Errorf("log retention variable = %q", got)
	}
}

func TestLoad_ReadsLogRetention(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.test")
	t.Setenv("LOG_RETENTION", "168h")
	t.Setenv("LOG_RETENTION_BY_PLAN", "free=24h")
	if got := Load().LogRetention; got.For(domain.Free) != 24*time.Hour || got.Default != 168*time.Hour {
		t.Errorf("loaded %+v", got)
	}
}
