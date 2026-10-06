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
