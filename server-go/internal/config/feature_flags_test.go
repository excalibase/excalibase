package config

import (
	"reflect"
	"testing"
	"time"
)

// Every boolean switch on AppConfig is a feature flag an operator can turn
// on, and each one has to be declared so the boot-time wiring check can
// refuse to start a feature whose dependencies are missing. A new flag that
// nobody declared fails here rather than shipping unwired.
func TestFeatureFlags_CoversEveryBooleanOnAppConfig(t *testing.T) {
	declared := map[string]Flag{}
	for _, f := range (AppConfig{}).FeatureFlags() {
		declared[f.Field] = f
	}
	cfgType := reflect.TypeOf(AppConfig{})
	for i := 0; i < cfgType.NumField(); i++ {
		field := cfgType.Field(i)
		if field.Type.Kind() != reflect.Bool {
			continue
		}
		flag, ok := declared[field.Name]
		if !ok {
			t.Errorf("AppConfig.%s is a feature flag with no FeatureFlags() entry", field.Name)
			continue
		}
		if flag.Env == "" {
			t.Errorf("flag %s declares no environment variable", field.Name)
		}
	}
}

// A flag's declared value must be the config's value — a declaration that
// reports a constant would make the wiring check meaningless.
func TestFeatureFlags_ReportTheConfiguredValue(t *testing.T) {
	cfg := AppConfig{SchedulerEnabled: true, AutoMigrate: false, AutoPauseEnabled: true}
	value := reflect.ValueOf(cfg)
	for _, flag := range cfg.FeatureFlags() {
		field := value.FieldByName(flag.Field)
		if !field.IsValid() {
			t.Errorf("flag %s names no field on AppConfig", flag.Field)
			continue
		}
		if field.Bool() != flag.Enabled {
			t.Errorf("flag %s: declared %v, field is %v", flag.Field, flag.Enabled, field.Bool())
		}
	}
}

func TestLoad_SchedulerDefaults(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.test")
	t.Setenv("EXCALIBASE_SCHEDULER_ENABLED", "")
	t.Setenv("EXCALIBASE_SCHEDULER_POLL_MS", "")
	t.Setenv("EXCALIBASE_CRON_POLL_MS", "")
	t.Setenv("EXCALIBASE_AUTO_MIGRATE", "")

	cfg := Load()
	if !cfg.SchedulerEnabled {
		t.Error("the function scheduler is on by default")
	}
	if !cfg.AutoMigrate {
		t.Error("schema auto-migration is on by default")
	}
	if cfg.SchedulerPollInterval != 5*time.Second {
		t.Errorf("poll interval: got %s, want 5s", cfg.SchedulerPollInterval)
	}
	if cfg.SchedulerCronInterval != 60*time.Second {
		t.Errorf("cron interval: got %s, want 60s", cfg.SchedulerCronInterval)
	}
}

func TestLoad_SchedulerOverrides(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.test")
	t.Setenv("EXCALIBASE_SCHEDULER_ENABLED", "false")
	t.Setenv("EXCALIBASE_SCHEDULER_POLL_MS", "1500")
	t.Setenv("EXCALIBASE_CRON_POLL_MS", "120000")
	t.Setenv("EXCALIBASE_AUTO_MIGRATE", "false")

	cfg := Load()
	if cfg.SchedulerEnabled {
		t.Error("EXCALIBASE_SCHEDULER_ENABLED=false must turn the scheduler off")
	}
	if cfg.AutoMigrate {
		t.Error("EXCALIBASE_AUTO_MIGRATE=false must turn auto-migration off")
	}
	if cfg.SchedulerPollInterval != 1500*time.Millisecond {
		t.Errorf("poll interval: got %s, want 1.5s", cfg.SchedulerPollInterval)
	}
	if cfg.SchedulerCronInterval != 120*time.Second {
		t.Errorf("cron interval: got %s, want 120s", cfg.SchedulerCronInterval)
	}
}

// An unreadable interval is refused rather than replaced with a default an
// operator did not choose.
func TestParseMillis_RejectsUnusableValues(t *testing.T) {
	if _, err := parseMillis("nope", time.Second); err == nil {
		t.Error("a non-numeric value must be an error")
	}
	if _, err := parseMillis("0", time.Second); err == nil {
		t.Error("a zero interval must be an error")
	}
	if _, err := parseMillis("-5", time.Second); err == nil {
		t.Error("a negative interval must be an error")
	}
	got, err := parseMillis("", 7*time.Second)
	if err != nil || got != 7*time.Second {
		t.Errorf("unset: got (%s, %v), want the fallback", got, err)
	}
}

func TestLoad_SchedulerLimitDefaults(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.test")
	cfg := Load()
	checks := map[string]int{
		"batch":               cfg.SchedulerBatch,
		"project concurrency": cfg.SchedulerProjectConcurrency,
		"global concurrency":  cfg.SchedulerGlobalConcurrency,
		"args bytes":          cfg.SchedulerMaxArgsBytes,
		"attempts":            cfg.SchedulerMaxAttempts,
		"cron jobs":           cfg.CronMaxJobsPerProject,
		"project db conns":    cfg.ProjectDBMaxOpenConns,
		"project db pools":    cfg.ProjectDBMaxPools,
	}
	for name, got := range checks {
		if got <= 0 {
			t.Errorf("%s limit: got %d, want a positive default", name, got)
		}
	}
	if cfg.CronMinInterval != time.Minute {
		t.Errorf("cron minimum: got %s, want 1m", cfg.CronMinInterval)
	}
}

// A limit an operator set but got wrong stops the process; it is never
// replaced with a default they did not choose.
func TestParsePositiveInt_RejectsUnusableValues(t *testing.T) {
	if _, err := parsePositiveInt("many", 4); err == nil {
		t.Error("a non-numeric limit must be an error")
	}
	if _, err := parsePositiveInt("0", 4); err == nil {
		t.Error("a zero limit must be an error")
	}
	if _, err := parsePositiveInt("-1", 4); err == nil {
		t.Error("a negative limit must be an error")
	}
	got, err := parsePositiveInt("", 4)
	if err != nil || got != 4 {
		t.Errorf("unset: got (%d, %v), want the fallback", got, err)
	}
}
