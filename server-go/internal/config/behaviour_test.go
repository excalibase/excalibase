package config

import (
	"context"
	"strings"
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

// A typo in a threshold must stop the boot while the flag is on: falling back to a
// default would hide the mistake, and the default may not be what the operator meant.
func TestValidate_ABadBehaviourSettingFailsBootWhileTheFlagIsOn(t *testing.T) {
	bad := map[string]string{
		"BEHAVIOUR_SUSTAINED_PERCENT": "abc", "BEHAVIOUR_SUSTAINED_MINUTES": "0", "BEHAVIOUR_WINDOW_MINUTES": "-5",
		"BEHAVIOUR_EVAL_MINUTES": "0", "BEHAVIOUR_GROWTH_FACTOR": "0.5", "BEHAVIOUR_TIER_MBPS": "free=fast",
		"BEHAVIOUR_SYMMETRY_FLOOR_KBPS": "free=lots", "BEHAVIOUR_DAILY_FLOOR_GB": "free", "BEHAVIOUR_SYMMETRY_DAY_HOURS": "25",
		"BEHAVIOUR_SYMMETRY_MIN_PERCENT": "x", "BEHAVIOUR_DAILY_VOLUME_PERCENT": "0", "BEHAVIOUR_MUTE_DAYS": "1.5",
	}
	for name, value := range bad {
		clearBehaviourEnv(t)
		t.Setenv("FEATURE_BEHAVIOURREVIEW", "true")
		t.Setenv(name, value)
		err := Load().Validate()
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s=%q: Validate() = %v, want an error naming the variable", name, value, err)
		}
		t.Setenv("FEATURE_BEHAVIOURREVIEW", "")
		if err := Load().Validate(); err != nil {
			t.Errorf("%s=%q with the flag off must not stop the boot: %v", name, value, err)
		}
	}
}

func TestValidate_IncoherentBehaviourSettingsFailBootWhileTheFlagIsOn(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"a backwards symmetry band":                  {"BEHAVIOUR_SYMMETRY_LOW": "2", "BEHAVIOUR_SYMMETRY_HIGH": "1"},
		"more sustained minutes than the window":     {"BEHAVIOUR_WINDOW_MINUTES": "30", "BEHAVIOUR_SUSTAINED_MINUTES": "45"},
		"a tier reference of zero":                   {"BEHAVIOUR_TIER_MBPS": "free=0"},
		"a tier pair with no name":                   {"BEHAVIOUR_TIER_MBPS": "=10"},
		"a day-hours count above the hours in a day": {"BEHAVIOUR_SYMMETRY_DAY_HOURS": "30"},
	} {
		clearBehaviourEnv(t)
		t.Setenv("FEATURE_BEHAVIOURREVIEW", "true")
		for k, v := range env {
			t.Setenv(k, v)
		}
		if err := Load().Validate(); err == nil || !strings.Contains(err.Error(), "BEHAVIOUR_") {
			t.Errorf("%s: Validate() = %v", name, err)
		}
	}
}

func TestValidate_GoodBehaviourSettingsPassWithTheFlagOn(t *testing.T) {
	clearBehaviourEnv(t)
	t.Setenv("FEATURE_BEHAVIOURREVIEW", "true")
	t.Setenv("BEHAVIOUR_SYMMETRY_FLOOR_KBPS", "free=15,standard=0")
	t.Setenv("BEHAVIOUR_DAILY_FLOOR_GB", "free=2.5")
	t.Setenv("BEHAVIOUR_SYMMETRY_DAY_HOURS", "8")
	if err := Load().Validate(); err != nil {
		t.Fatal(err)
	}
	got := Load().Behaviour
	if got.SymmetryFloorKBps["free"] != 15 || got.SymmetryFloorKBps["standard"] != 0 || got.DailyFloorGB["free"] != 2.5 || got.SymmetryDayHours != 8 {
		t.Errorf("settings = %+v", got)
	}
}

func TestLoad_TheAbsoluteFloorsHaveDocumentedDefaults(t *testing.T) {
	clearBehaviourEnv(t)
	got := Load().Behaviour
	if got.SymmetryFloorKBps["free"] != 10 || got.SymmetryDayHours != 6 || got.DailyFloorGB["free"] != 3 || got.SymmetryLow != 0.5 || got.SymmetryHigh != 2 {
		t.Errorf("defaults = %+v", got)
	}
}
