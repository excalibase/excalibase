// Package wiring is the boot-time check that an enabled feature has the
// collaborators it needs. Features used to guard themselves — one logged a
// warning and ran nothing, one failed its constructor, one was simply
// absent — so whether a switch did anything depended on which subsystem
// happened to own it. Here every flag declares its dependencies in one
// table, main runs the check once before serving, and a flag with no entry
// fails the package's own test.
package wiring

import (
	"fmt"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/config"
)

// Dependency is one collaborator a feature cannot work without.
type Dependency struct {
	// Name is what an operator reading the failure needs to recognise.
	Name string
	// Wired is whether main actually built it.
	Wired bool
}

// Feature ties one operator switch to the dependencies it needs when on.
type Feature struct {
	Flag     string
	Enabled  bool
	Requires []Dependency
	// Undeclared marks a config flag the table says nothing about. Such a
	// flag is exactly the hole this package exists to close, so an enabled
	// one fails the check rather than passing it by default.
	Undeclared bool
}

// Deps is what main has actually built by the time the check runs. Plain
// booleans rather than the objects themselves: the check asks whether a
// dependency exists, never what it does.
type Deps struct {
	// SchedulerInvoker dispatches a due task to a project's runtime.
	SchedulerInvoker bool
	// SchedulerProjects lists the projects whose databases may be swept.
	SchedulerProjects bool
	// ProjectDB opens a project's own database — the schema migrator, the
	// cron sync and the scheduler sweep all need it.
	ProjectDB bool
	// CronLeader elects the single replica that enqueues cron jobs.
	CronLeader bool
	// PauseService performs the idle pause the auto-pause sweep decides on.
	PauseService bool
	// FunctionRuntime is the runtime the cold-start replay re-deploys into.
	FunctionRuntime bool
}

// table declares, for every flag the platform has, what turning it on
// requires. A flag that genuinely needs nothing says so with an empty list;
// a flag missing from this map is undeclared, not dependency-free.
func table(deps Deps) map[string][]Dependency {
	return map[string][]Dependency{
		"EXCALIBASE_SCHEDULER_ENABLED": {
			{Name: "function scheduler invoker", Wired: deps.SchedulerInvoker},
			{Name: "project list", Wired: deps.SchedulerProjects},
			{Name: "project database resolver", Wired: deps.ProjectDB},
			{Name: "cron leader election", Wired: deps.CronLeader},
		},
		"EXCALIBASE_AUTO_MIGRATE": {
			{Name: "project database resolver", Wired: deps.ProjectDB},
		},
		"EXCALIBASE_AUTOPAUSE_ENABLED": {
			{Name: "pause service", Wired: deps.PauseService},
		},
		"EXCALIBASE_FN_REPLAY_ENABLED": {
			{Name: "function runtime", Wired: deps.FunctionRuntime},
		},
		// Checks the request path makes for itself — nothing to wire.
		"JWT_REQUIRE_AUD": {},
		// The exposure filter runs inside the engine on a list this service
		// serves; turning it off changes what that list says, not what has
		// to be wired here.
		"EXCALIBASE_EXPOSURE_ENFORCED": {},
		// Client and daemon settings, not subsystems.
		"KUBE_INSECURE_SKIP_VERIFY": {},
		"DOCKER_DB_PUBLIC":          {},
		"DOCKER_TLS_VERIFY":         {},
	}
}

// Features pairs every flag the config carries with its declared
// requirements, and adds the provider selections — a named provider must
// arrive with the settings it cannot work without.
func Features(cfg config.AppConfig, deps Deps) []Feature {
	declared := table(deps)
	flags := cfg.FeatureFlags()
	features := make([]Feature, 0, len(flags))
	for _, flag := range flags {
		requires, known := declared[flag.Env]
		features = append(features, Feature{
			Flag:       flag.Env,
			Enabled:    flag.Enabled,
			Requires:   requires,
			Undeclared: !known,
		})
	}
	return append(features, providerFeatures(cfg)...)
}

// providerFeatures covers the switches that name a provider rather than
// turning a boolean on. Each is "enabled" when the deployment selected it,
// and requires the settings that provider needs. A half-configured provider
// used to degrade to a no-op sender or a disabled feature at runtime, which
// an operator only discovers when a user does.
func providerFeatures(cfg config.AppConfig) []Feature {
	set := func(values ...string) bool {
		for _, v := range values {
			if v == "" {
				return false
			}
		}
		return true
	}
	any := func(values ...string) bool {
		for _, v := range values {
			if v != "" {
				return true
			}
		}
		return false
	}
	return []Feature{
		{
			Flag:    "EMAIL_PROVIDER=ses",
			Enabled: cfg.EmailProvider == "ses",
			Requires: []Dependency{
				{Name: "SES credentials (SES_ACCESS_KEY_ID, SES_SECRET_ACCESS_KEY)",
					Wired: set(cfg.SESAccessKeyID, cfg.SESSecretAccessKey)},
				{Name: "SES region", Wired: set(cfg.SESRegion)},
			},
		},
		{
			Flag:    "EMAIL_PROVIDER=resend",
			Enabled: cfg.EmailProvider == "resend",
			Requires: []Dependency{
				{Name: "Resend credential (RESEND_API_KEY)", Wired: set(cfg.ResendAPIKey)},
			},
		},
		{
			Flag:    "EMAIL_PROVIDER",
			Enabled: cfg.EmailProvider != "",
			Requires: []Dependency{
				{Name: "a known email provider (ses, resend or noop)",
					Wired: cfg.EmailProvider == "ses" || cfg.EmailProvider == "resend" || cfg.EmailProvider == "noop"},
			},
		},
		{
			Flag:    "R2_*",
			Enabled: any(cfg.R2AccessKeyID, cfg.R2SecretAccessKey, cfg.R2Endpoint, cfg.R2Bucket),
			Requires: []Dependency{
				{Name: "complete object-storage settings (R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY, R2_ENDPOINT, R2_BUCKET)",
					Wired: set(cfg.R2AccessKeyID, cfg.R2SecretAccessKey, cfg.R2Endpoint, cfg.R2Bucket)},
			},
		},
		{
			Flag:    "BACKUP_DEFAULT_*",
			Enabled: any(cfg.BackupAccessKeyID, cfg.BackupSecretAccessKey, cfg.BackupEndpoint),
			Requires: []Dependency{
				{Name: "complete backup destination (access key, secret, endpoint, bucket)",
					Wired: set(cfg.BackupAccessKeyID, cfg.BackupSecretAccessKey, cfg.BackupEndpoint, cfg.BackupBucket)},
			},
		},
	}
}

// Check reports every enabled feature whose dependencies are missing. One
// error lists them all so a misconfigured deployment is fixed in one pass.
func Check(features []Feature) error {
	var gaps []string
	for _, f := range features {
		if !f.Enabled {
			continue
		}
		if f.Undeclared {
			gaps = append(gaps, fmt.Sprintf("%s is on but has no wiring entry", f.Flag))
			continue
		}
		for _, req := range f.Requires {
			if !req.Wired {
				gaps = append(gaps, fmt.Sprintf("%s is on but %s is not wired", f.Flag, req.Name))
			}
		}
	}
	if len(gaps) == 0 {
		return nil
	}
	return fmt.Errorf("startup wiring check failed: %s", strings.Join(gaps, "; "))
}
