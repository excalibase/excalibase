package config

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/behaviour"
	"github.com/excalibase/provisioning-poc/internal/features"
)

func clearBehaviourEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CORS_ORIGINS", "https://app.test")
	for _, name := range behaviourEnvNames {
		t.Setenv(name, "")
	}
}

// EXC-844: behaviour review ships dark and runs on every mode.
func TestLoad_BehaviourReviewIsOffUntilItsVariableSaysTrue(t *testing.T) {
	clearBehaviourEnv(t)
	for _, mode := range []string{"", "docker"} {
		t.Setenv("PROVISIONER_MODE", mode)
		for value, want := range map[string]bool{"": false, "yes": false, "false": false, "true": true} {
			t.Setenv("FEATURE_BEHAVIOURREVIEW", value)
			if got := Load().Features().Enabled(context.Background(), features.BehaviourReview); got != want {
				t.Errorf("mode %q FEATURE_BEHAVIOURREVIEW=%q: enabled=%v, want %v", mode, value, got, want)
			}
		}
	}
}

func TestLoad_BehaviourSettingsDefaultToTheDocumentedOnes(t *testing.T) {
	clearBehaviourEnv(t)
	got := Load().Behaviour
	want := behaviour.DefaultConfig()
	if got.WindowMinutes != want.WindowMinutes || got.SustainedPercent != want.SustainedPercent || got.SustainedMinutes != want.SustainedMinutes ||
		got.SymmetryLow != want.SymmetryLow || got.SymmetryHigh != want.SymmetryHigh || got.EvalEvery != want.EvalEvery ||
		got.ReferenceMbps["free"] != 10 || got.DefaultReferenceMbps != 100 || got.MuteDays != 14 || got.GrowthFactor != 2 {
		t.Errorf("defaults = %+v", got)
	}
}

func TestLoad_BehaviourSettingsComeFromTheEnvironment(t *testing.T) {
	clearBehaviourEnv(t)
	t.Setenv("BEHAVIOUR_SUSTAINED_PERCENT", "65")
	t.Setenv("BEHAVIOUR_SUSTAINED_MINUTES", "30")
	t.Setenv("BEHAVIOUR_SYMMETRY_LOW", "0.7")
	t.Setenv("BEHAVIOUR_SYMMETRY_HIGH", "1.4")
	t.Setenv("BEHAVIOUR_EVAL_MINUTES", "10")
	t.Setenv("BEHAVIOUR_TIER_MBPS", " free = 20 , standard=250,enterprise=2000 ")
	t.Setenv("BEHAVIOUR_DEFAULT_MBPS", "50")
	t.Setenv("BEHAVIOUR_MUTE_DAYS", "7")
	got := Load().Behaviour
	if got.SustainedPercent != 65 || got.SustainedMinutes != 30 || got.SymmetryLow != 0.7 || got.SymmetryHigh != 1.4 || got.EvalEvery != 10*time.Minute ||
		got.ReferenceMbps["free"] != 20 || got.ReferenceMbps["standard"] != 250 || got.ReferenceMbps["enterprise"] != 2000 || got.DefaultReferenceMbps != 50 || got.MuteDays != 7 {
		t.Errorf("settings = %+v", got)
	}
}

func TestLoad_ABadBehaviourSettingKeepsItsDefaultNotZero(t *testing.T) {
	for name, value := range map[string]string{
		"BEHAVIOUR_SUSTAINED_PERCENT": "abc", "BEHAVIOUR_SUSTAINED_MINUTES": "0", "BEHAVIOUR_WINDOW_MINUTES": "-5",
		"BEHAVIOUR_EVAL_MINUTES": "0", "BEHAVIOUR_GROWTH_FACTOR": "0.5", "BEHAVIOUR_TIER_MBPS": "free=fast",
	} {
		clearBehaviourEnv(t)
		t.Setenv(name, value)
		got, want := Load().Behaviour, behaviour.DefaultConfig()
		if got.SustainedPercent != want.SustainedPercent || got.SustainedMinutes != want.SustainedMinutes || got.WindowMinutes != want.WindowMinutes ||
			got.EvalEvery != want.EvalEvery || got.GrowthFactor != want.GrowthFactor || got.ReferenceMbps["free"] != want.ReferenceMbps["free"] {
			t.Errorf("%s=%q: %+v", name, value, got)
		}
	}
}

func TestLoad_ASymmetryBandThatIsBackwardsFallsBackAsAPair(t *testing.T) {
	clearBehaviourEnv(t)
	t.Setenv("BEHAVIOUR_SYMMETRY_LOW", "2")
	t.Setenv("BEHAVIOUR_SYMMETRY_HIGH", "1")
	got, want := Load().Behaviour, behaviour.DefaultConfig()
	if got.SymmetryLow != want.SymmetryLow || got.SymmetryHigh != want.SymmetryHigh {
		t.Errorf("band = %v..%v", got.SymmetryLow, got.SymmetryHigh)
	}
}

func TestLoad_ASustainedMinutesCountLargerThanTheWindowIsRefused(t *testing.T) {
	clearBehaviourEnv(t)
	t.Setenv("BEHAVIOUR_WINDOW_MINUTES", "30")
	t.Setenv("BEHAVIOUR_SUSTAINED_MINUTES", "45")
	got, want := Load().Behaviour, behaviour.DefaultConfig()
	if got.WindowMinutes != want.WindowMinutes || got.SustainedMinutes != want.SustainedMinutes {
		t.Errorf("a signal that could never fire was accepted: %d of %d", got.SustainedMinutes, got.WindowMinutes)
	}
}

func TestLoad_ATierReferenceOfZeroOrLessIsRefused(t *testing.T) {
	for _, value := range []string{"free=0", "free=-5", "free=10,standard=0", "=10", "free", ""} {
		clearBehaviourEnv(t)
		t.Setenv("BEHAVIOUR_TIER_MBPS", value)
		if got := Load().Behaviour.ReferenceMbps; got["free"] != 10 || got["standard"] != 100 {
			t.Errorf("BEHAVIOUR_TIER_MBPS=%q: %+v", value, got)
		}
	}
}
