package wiring

import (
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
)

// fullyWired is every dependency present.
func fullyWired() Deps {
	return Deps{
		SchedulerInvoker:  true,
		SchedulerProjects: true,
		ProjectDB:         true,
		CronLeader:        true,
		PauseService:      true,
	}
}

// allOn turns on every flag so a missing dependency has something to fail.
func allOn() config.AppConfig {
	return config.AppConfig{
		SchedulerEnabled: true,
		AutoMigrate:      true,
		AutoPauseEnabled: true,
		JWTRequireAud:    true,
	}
}

func TestCheck_PassesWhenEverythingIsWired(t *testing.T) {
	if err := Check(Features(allOn(), fullyWired())); err != nil {
		t.Fatalf("fully wired: %v", err)
	}
}

// The general guard: an enabled feature whose dependency is nil must stop
// the process, naming the flag and what is missing.
func TestCheck_FailsForEachEnabledButUnwiredFeature(t *testing.T) {
	cases := map[string]func(*Deps){
		"EXCALIBASE_SCHEDULER_ENABLED": func(d *Deps) { d.SchedulerInvoker = false },
		"EXCALIBASE_AUTO_MIGRATE":      func(d *Deps) { d.ProjectDB = false },
		"EXCALIBASE_AUTOPAUSE_ENABLED": func(d *Deps) { d.PauseService = false },
	}
	for flag, strip := range cases {
		t.Run(flag, func(t *testing.T) {
			deps := fullyWired()
			strip(&deps)
			err := Check(Features(allOn(), deps))
			if err == nil {
				t.Fatalf("%s enabled with a missing dependency must fail the boot", flag)
			}
			if !strings.Contains(err.Error(), flag) {
				t.Errorf("err must name the flag: %v", err)
			}
		})
	}
}

// A feature nobody turned on needs nothing wired.
func TestCheck_IgnoresDisabledFeatures(t *testing.T) {
	if err := Check(Features(config.AppConfig{}, Deps{})); err != nil {
		t.Fatalf("everything off: %v", err)
	}
}

// One error names every gap, so an operator fixes the deployment once
// instead of restarting into the next missing dependency.
func TestCheck_ReportsEveryGapAtOnce(t *testing.T) {
	err := Check(Features(allOn(), Deps{}))
	if err == nil {
		t.Fatal("nothing wired: want an error")
	}
	for _, flag := range []string{
		"EXCALIBASE_SCHEDULER_ENABLED", "EXCALIBASE_AUTO_MIGRATE", "EXCALIBASE_AUTOPAUSE_ENABLED",
	} {
		if !strings.Contains(err.Error(), flag) {
			t.Errorf("err must name %s: %v", flag, err)
		}
	}
}

// The mechanism only holds if every flag is in it: a flag with no entry
// could be enabled with nothing wired and nobody would notice.
func TestFeatures_CoverEveryConfigFlag(t *testing.T) {
	fields := map[string]string{}
	for _, flag := range (config.AppConfig{}).FeatureFlags() {
		fields[flag.Env] = flag.Field
	}
	for _, f := range Features(config.AppConfig{}, Deps{}) {
		if f.Undeclared {
			t.Errorf("config flag %s (%s) has no wiring entry", f.Flag, fields[f.Flag])
		}
	}
}

// An undeclared flag that is switched on stops the boot: the table, not a
// default, decides what a flag needs.
func TestCheck_FailsForAFlagWithNoWiringEntry(t *testing.T) {
	err := Check([]Feature{{Flag: "EXCALIBASE_NEW_THING", Enabled: true, Undeclared: true}})
	if err == nil || !strings.Contains(err.Error(), "no wiring entry") {
		t.Fatalf("err: got %v, want the undeclared flag refused", err)
	}
}

// A feature may legitimately need nothing — but it says so by being listed
// with no requirements, not by being absent.
func TestFeatures_EveryEntryNamesItsRequirements(t *testing.T) {
	for _, f := range Features(allOn(), fullyWired()) {
		if f.Flag == "" {
			t.Error("a feature entry with no flag name")
		}
		for _, req := range f.Requires {
			if req.Name == "" {
				t.Errorf("%s: a requirement with no name", f.Flag)
			}
		}
	}
}

// A provider the operator named must arrive with what it needs. Half of a
// provider's settings used to degrade to a no-op sender or a disabled
// feature, which an operator only learns about when a user hits it.
func TestCheck_ProviderSelectedWithoutItsSettings(t *testing.T) {
	cases := map[string]config.AppConfig{
		"EMAIL_PROVIDER=ses":    {EmailProvider: "ses"},
		"EMAIL_PROVIDER=resend": {EmailProvider: "resend"},
		"R2_*":                  {R2Bucket: "uploads"},
		"BACKUP_DEFAULT_*":      {BackupEndpoint: "https://r2", BackupBucket: "backups"},
	}
	for flag, cfg := range cases {
		t.Run(flag, func(t *testing.T) {
			err := Check(Features(cfg, fullyWired()))
			if err == nil {
				t.Fatalf("%s named without its settings must fail the boot", flag)
			}
			if !strings.Contains(err.Error(), flag) {
				t.Errorf("err must name the setting: %v", err)
			}
		})
	}
}

// An unknown provider name is refused rather than quietly becoming a no-op.
func TestCheck_UnknownEmailProvider(t *testing.T) {
	err := Check(Features(config.AppConfig{EmailProvider: "sendmail"}, fullyWired()))
	if err == nil || !strings.Contains(err.Error(), "EMAIL_PROVIDER") {
		t.Fatalf("err: got %v, want the unknown provider refused", err)
	}
}

// A fully configured provider passes, and a deployment that named none is
// left alone.
func TestCheck_ProvidersConfigured(t *testing.T) {
	cfg := config.AppConfig{
		EmailProvider: "resend", ResendAPIKey: "re_x",
		R2AccessKeyID: "k", R2SecretAccessKey: "s", R2Endpoint: "https://r2", R2Bucket: "uploads",
		BackupAccessKeyID: "k", BackupSecretAccessKey: "s", BackupEndpoint: "https://r2", BackupBucket: "backups",
	}
	if err := Check(Features(cfg, fullyWired())); err != nil {
		t.Fatalf("fully configured: %v", err)
	}
	if err := Check(Features(config.AppConfig{}, Deps{})); err != nil {
		t.Fatalf("nothing named: %v", err)
	}
}
