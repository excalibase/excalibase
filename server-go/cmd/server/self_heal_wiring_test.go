package main

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/service"
)

func TestSelfHeal_WantedOnlyWithItsFlag(t *testing.T) {
	if selfHealOn(config.AppConfig{}, nil) || selfHealOn(config.AppConfig{}, features.NewStatic(features.Pipeline)) {
		t.Error("self-heal is on without its flag")
	}
	if !selfHealOn(config.AppConfig{}, features.NewStatic(features.SelfHeal)) {
		t.Error("self-heal is off with its flag")
	}
	alerts := service.NewAlertingService(t.TempDir())
	if newRetryEscalator(config.AppConfig{}, features.NewStatic(), alerts, nil) != nil {
		t.Error("an escalator exists while the flag is off")
	}
	if newRetryEscalator(config.AppConfig{}, features.NewStatic(features.SelfHeal), alerts, nil) == nil {
		t.Error("no escalator while the flag is on")
	}
}

// The sweep that retries a stuck pause runs with auto-pause off once self-heal
// is on, and then judges no project idle.
func TestSelfHeal_SweepRunsWithAutoPauseOffOnlyWhenSelfHealIsOn(t *testing.T) {
	cases := []struct {
		autoPause, selfHeal bool
		wantRun, wantIdle   bool
	}{
		{false, false, false, false},
		{true, false, true, true},
		{false, true, true, false},
		{true, true, true, true},
	}
	for _, tc := range cases {
		cfg := config.AppConfig{AutoPauseEnabled: tc.autoPause}
		flags := features.NewStatic()
		if tc.selfHeal {
			flags = features.NewStatic(features.SelfHeal)
		}
		run, idle := idleSweepModes(cfg, flags)
		if run != tc.wantRun || idle != tc.wantIdle {
			t.Errorf("autopause=%v selfheal=%v: run=%v idle=%v, want %v %v", tc.autoPause, tc.selfHeal, run, idle, tc.wantRun, tc.wantIdle)
		}
	}
}
