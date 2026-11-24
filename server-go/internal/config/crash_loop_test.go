package config

import (
	"reflect"
	"testing"
)

func TestLoadCrashLoop_Defaults(t *testing.T) {
	t.Setenv("APP_CRASH_LOOP_MAX_PER_HOUR", "")
	t.Setenv("CRASH_GUARD_EXEMPT_PROJECTS", "")
	got := loadCrashLoop()
	if got.MaxPerHour != 10 {
		t.Errorf("MaxPerHour = %d, want 10", got.MaxPerHour)
	}
	if !reflect.DeepEqual(got.ExemptProjects, []string{"proj-jfp7kx46kb"}) {
		t.Errorf("ExemptProjects = %v, want the one protected project", got.ExemptProjects)
	}
}

func TestLoadCrashLoop_ReadsTheEnvironment(t *testing.T) {
	t.Setenv("APP_CRASH_LOOP_MAX_PER_HOUR", "25")
	t.Setenv("CRASH_GUARD_EXEMPT_PROJECTS", "proj-a, proj-b")
	got := loadCrashLoop()
	if got.MaxPerHour != 25 || !reflect.DeepEqual(got.ExemptProjects, []string{"proj-a", "proj-b"}) {
		t.Errorf("got %+v", got)
	}
}

func TestLoadCrashLoop_ANonPositiveThresholdKeepsTheDefault(t *testing.T) {
	for _, raw := range []string{"0", "-3", "many"} {
		t.Setenv("APP_CRASH_LOOP_MAX_PER_HOUR", raw)
		if got := loadCrashLoop().MaxPerHour; got != 10 {
			t.Errorf("%q gave %d, want 10", raw, got)
		}
	}
}

func TestLoadCrashLoop_AnExplicitlyEmptyExemptionListIsHonoured(t *testing.T) {
	t.Setenv("CRASH_GUARD_EXEMPT_PROJECTS", "none")
	if got := loadCrashLoop().ExemptProjects; len(got) != 0 {
		t.Errorf("ExemptProjects = %v, want none", got)
	}
}
