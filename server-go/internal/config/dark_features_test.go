package config

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/features"
)

// EXC-554: a feature that ships dark is off until its variable says exactly "true".
func TestLoad_DarkFeaturesDefaultOff(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.test")
	for _, env := range featureEnv {
		t.Setenv(env, "")
	}
	flags := Load().Features()
	for _, feature := range features.All() {
		if flags.Enabled(context.Background(), feature) != defaultOn[feature] {
			t.Errorf("%s is %v with nothing set", feature, !defaultOn[feature])
		}
	}
}

// EXC-755: the abuse guards are security fixes, so they are on until an install says "false".
func TestLoad_AbuseGuardsAreOnUnlessSwitchedOff(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.test")
	for _, feature := range []features.Feature{features.ProxyBlock, features.SignupGuard} {
		env := featureEnv[feature]
		for value, want := range map[string]bool{"": true, "true": true, "yes": true, "false": false} {
			t.Setenv(env, value)
			if got := Load().Features().Enabled(context.Background(), feature); got != want {
				t.Errorf("%s=%q: enabled=%v, want %v", env, value, got, want)
			}
		}
	}
}

// EXC-759: showing a password once is a security fix, so it is on unless switched off.
func TestLoad_OneTimeCredentialsAreOnUnlessSwitchedOff(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.test")
	t.Setenv("FEATURE_ONETIMECREDENTIALS", "")
	if !Load().Features().Enabled(context.Background(), features.OneTimeCredentials) {
		t.Error("one-time credentials are off with nothing set")
	}
	t.Setenv("FEATURE_ONETIMECREDENTIALS", "false")
	if Load().Features().Enabled(context.Background(), features.OneTimeCredentials) {
		t.Error("FEATURE_ONETIMECREDENTIALS=false did not switch them off")
	}
}

// MCP's destructive tools asking twice is a safety rail, so it is on unless an install says "false".
func TestLoad_MCPConfirmIsOnUnlessSwitchedOff(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.test")
	for value, want := range map[string]bool{"": true, "true": true, "yes": true, "false": false} {
		t.Setenv("FEATURE_MCPCONFIRM", value)
		if got := Load().Features().Enabled(context.Background(), features.MCPConfirm); got != want {
			t.Errorf("FEATURE_MCPCONFIRM=%q: enabled=%v, want %v", value, got, want)
		}
	}
}

func TestLoad_EachDarkFeatureIsTurnedOnByItsOwnVariable(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.test")
	for feature, env := range featureEnv {
		t.Run(string(feature), func(t *testing.T) {
			for _, other := range featureEnv {
				t.Setenv(other, "")
			}
			if defaultOn[feature] {
				t.Skip("on without its variable; covered by TestLoad_AbuseGuardsAreOnUnlessSwitchedOff")
			}
			t.Setenv(env, "true")
			if feature == features.HostMetrics || feature == features.HostLogs {
				t.Setenv("PROVISIONER_MODE", "docker")
			}
			flags := Load().Features()
			for _, each := range features.All() {
				if got := flags.Enabled(context.Background(), each); got != (each == feature || defaultOn[each]) {
					t.Errorf("%s=true: %s enabled=%v", env, each, got)
				}
			}
			t.Setenv(env, "yes")
			if !defaultOn[feature] && Load().Features().Enabled(context.Background(), feature) {
				t.Errorf("%s=yes: only the exact value \"true\" turns a dark feature on", env)
			}
		})
	}
}

// Every declared feature has a variable that turns it on.
func TestDarkFeatures_EveryDeclaredFeatureHasAVariable(t *testing.T) {
	for _, feature := range features.All() {
		if featureEnv[feature] == "" {
			t.Errorf("feature %s has no environment variable", feature)
		}
	}
	if len(featureEnv) != len(features.All()) {
		t.Errorf("%d variables for %d declared features", len(featureEnv), len(features.All()))
	}
}

// EXC-657: the chart sets this exact name from features.metricsEndpoint.
func TestDarkFeatures_MetricsEndpointVariable(t *testing.T) {
	if got := featureEnv[features.MetricsEndpoint]; got != "FEATURE_METRICSENDPOINT" {
		t.Errorf("metrics endpoint variable = %q", got)
	}
}

// EXC-658: the chart sets this exact name from features.logDrains.
func TestDarkFeatures_LogDrainsVariable(t *testing.T) {
	if got := featureEnv[features.LogDrains]; got != "FEATURE_LOGDRAINS" {
		t.Errorf("log drains variable = %q", got)
	}
}

// EXC-662: the chart and the single-host compose file set this exact name.
func TestDarkFeatures_AuthOAuthVariable(t *testing.T) {
	if got := featureEnv[features.AuthOAuth]; got != "FEATURE_AUTHOAUTH" {
		t.Errorf("auth oauth variable = %q", got)
	}
}

// EXC-662: the chart and the single-host compose file set this exact name.
func TestDarkFeatures_AuthOTPVariable(t *testing.T) {
	if got := featureEnv[features.AuthOTP]; got != "FEATURE_AUTHOTP" {
		t.Errorf("auth otp variable = %q", got)
	}
}

// EXC-662: the chart and the single-host compose file set this exact name.
func TestDarkFeatures_AuthUserVariable(t *testing.T) {
	if got := featureEnv[features.AuthUser]; got != "FEATURE_AUTHUSER" {
		t.Errorf("auth user variable = %q", got)
	}
}

// EXC-670: the single-host compose file sets this exact name; Kubernetes ignores it.
func TestDarkFeatures_HostMetricsIsOnOnlyOnASingleHost(t *testing.T) {
	if got := featureEnv[features.HostMetrics]; got != "FEATURE_HOSTMETRICS" {
		t.Errorf("host metrics variable = %q", got)
	}
	on := AppConfig{DarkFeatures: []features.Feature{features.HostMetrics, features.MCP}}
	for mode, want := range map[string]bool{"docker": true, "kubernetes": false, "": false} {
		on.ProvisionerMode = mode
		flags := on.Features()
		if flags.Enabled(context.Background(), features.HostMetrics) != want || !flags.Enabled(context.Background(), features.MCP) {
			t.Errorf("mode %q: hostmetrics=%v mcp=%v", mode, flags.Enabled(context.Background(), features.HostMetrics), flags.Enabled(context.Background(), features.MCP))
		}
	}
}

// EXC-662: the chart and the single-host compose file set this exact name.
func TestDarkFeatures_AuthHooksVariable(t *testing.T) {
	if got := featureEnv[features.AuthHooks]; got != "FEATURE_AUTHHOOKS" {
		t.Errorf("auth hooks variable = %q", got)
	}
}

// EXC-662: the chart and the single-host compose file set this exact name.
func TestDarkFeatures_AuthMFAVariable(t *testing.T) {
	if got := featureEnv[features.AuthMFA]; got != "FEATURE_AUTHMFA" {
		t.Errorf("auth mfa variable = %q", got)
	}
}

// EXC-667: the chart and single-host compose set this exact name.
func TestDarkFeatures_DBCopyVariable(t *testing.T) {
	if got := featureEnv[features.DBCopy]; got != "FEATURE_DBCOPY" {
		t.Errorf("dbcopy variable = %q, want FEATURE_DBCOPY", got)
	}
}

// EXC-668: the chart sets this exact name from features.majorUpgrade.
func TestDarkFeatures_MajorUpgradeVariable(t *testing.T) {
	if got := featureEnv[features.MajorUpgrade]; got != "FEATURE_MAJORUPGRADE" {
		t.Fatalf("major upgrade variable = %q, want FEATURE_MAJORUPGRADE", got)
	}
}

// EXC-669: the chart and the single-host compose file set this exact name.
func TestDarkFeatures_TemplateCatalogVariable(t *testing.T) {
	if got := featureEnv[features.TemplateCatalog]; got != "FEATURE_TEMPLATECATALOG" {
		t.Errorf("template catalog variable = %q", got)
	}
}

// EXC-672: the single-host compose file sets this exact name; Kubernetes has its own Loki.
func TestDarkFeatures_HostLogsIsOnOnlyOnASingleHost(t *testing.T) {
	if got := featureEnv[features.HostLogs]; got != "FEATURE_HOSTLOGS" {
		t.Errorf("host logs variable = %q", got)
	}
	on := AppConfig{DarkFeatures: []features.Feature{features.HostLogs, features.LogRetention}}
	for mode, want := range map[string]bool{"docker": true, "kubernetes": false, "": false} {
		on.ProvisionerMode = mode
		flags := on.Features()
		if flags.Enabled(context.Background(), features.HostLogs) != want || !flags.Enabled(context.Background(), features.LogRetention) {
			t.Errorf("mode %q: hostlogs=%v logretention=%v", mode, flags.Enabled(context.Background(), features.HostLogs), flags.Enabled(context.Background(), features.LogRetention))
		}
	}
}

// EXC-671: the single-host compose file passes this exact name through.
func TestDarkFeatures_SingleHostBuildsVariable(t *testing.T) {
	if got := featureEnv[features.SingleHostBuilds]; got != "FEATURE_SINGLEHOSTBUILDS" {
		t.Errorf("single-host builds variable = %q", got)
	}
}

func TestLoad_SignupsPerHour(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.test")
	if got := Load().SignupsPerHour; got != 10 {
		t.Errorf("default = %d, want 10", got)
	}
	t.Setenv("SIGNUPS_PER_NETWORK_PER_HOUR", "3")
	if got := Load().SignupsPerHour; got != 3 {
		t.Errorf("SIGNUPS_PER_NETWORK_PER_HOUR=3: %d", got)
	}
}

// EXC-784: the archive alert and Free's hourly upload are safety fixes, so they are on unless switched off.
func TestLoad_WALGuardsAreOnUnlessSwitchedOff(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.test")
	for feature, env := range map[features.Feature]string{
		features.WALArchiveAlert:   "FEATURE_WALARCHIVEALERT",
		features.FreeWALUpload:     "FEATURE_FREEWALUPLOAD",
		features.AppRuntimeLogin:   "FEATURE_APPRUNTIMELOGIN",
		features.FunctionHTMLGuard: "FEATURE_FUNCTIONHTMLGUARD",
	} {
		t.Setenv(env, "")
		if !Load().Features().Enabled(context.Background(), feature) {
			t.Errorf("%s is off with nothing set", feature)
		}
		t.Setenv(env, "false")
		if Load().Features().Enabled(context.Background(), feature) {
			t.Errorf("%s=false did not switch it off", env)
		}
	}
}

func TestLoad_WALArchiveAlertThreshold(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.test")
	t.Setenv("EXCALIBASE_WAL_ARCHIVE_ALERT_AFTER", "")
	if got := Load().WALArchiveAlertAfter; got != 30*time.Minute {
		t.Errorf("default = %v, want 30m", got)
	}
	t.Setenv("EXCALIBASE_WAL_ARCHIVE_ALERT_AFTER", "45m")
	if got := Load().WALArchiveAlertAfter; got != 45*time.Minute {
		t.Errorf("configured = %v, want 45m", got)
	}
}
