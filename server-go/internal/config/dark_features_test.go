package config

import (
	"context"
	"testing"

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
		if flags.Enabled(context.Background(), feature) {
			t.Errorf("%s is on with nothing set", feature)
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
			t.Setenv(env, "true")
			if feature == features.HostMetrics {
				t.Setenv("PROVISIONER_MODE", "docker")
			}
			flags := Load().Features()
			for _, each := range features.All() {
				if got := flags.Enabled(context.Background(), each); got != (each == feature) {
					t.Errorf("%s=true: %s enabled=%v", env, each, got)
				}
			}
			t.Setenv(env, "yes")
			if Load().Features().Enabled(context.Background(), feature) {
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
