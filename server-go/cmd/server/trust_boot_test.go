package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/trust"
)

type platformWithoutMarker struct{}

type platformWithMarker struct{ run *trust.BackfillRun }

type platformThatHangs struct{}

func (platformThatHangs) LatestBackfill(ctx context.Context) (*trust.BackfillRun, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (p platformWithMarker) LatestBackfill(context.Context) (*trust.BackfillRun, error) {
	return p.run, nil
}

func trustCfg(t *testing.T, flag string) config.AppConfig {
	t.Helper()
	t.Setenv("CORS_ORIGINS", "https://app.test")
	t.Setenv("FEATURE_LIMITEDTRIAL", flag)
	return config.Load()
}

// FEATURE_LIMITEDTRIAL=true with no recorded backfill would limit every existing account.
func TestBootRefusesTheTrialFlagWithoutABackfill(t *testing.T) {
	if err := trustBackfillCheck(trustCfg(t, "true"), platformWithoutMarker{}); err == nil {
		t.Error("boot allowed the flag with a platform store that cannot record a backfill")
	}
	if err := trustBackfillCheck(trustCfg(t, "true"), platformWithMarker{}); err == nil {
		t.Error("boot allowed the flag with no backfill marker")
	}
}

func TestBootAllowsTheTrialFlagAfterABackfill(t *testing.T) {
	run := &trust.BackfillRun{FinishedAt: time.Now(), Cutoff: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
	if err := trustBackfillCheck(trustCfg(t, "true"), platformWithMarker{run: run}); err != nil {
		t.Errorf("refused after a backfill: %v", err)
	}
}

func TestBootIgnoresTheBackfillWhileTheFlagIsOff(t *testing.T) {
	if err := trustBackfillCheck(trustCfg(t, ""), platformWithoutMarker{}); err != nil {
		t.Errorf("flag off still demanded a backfill: %v", err)
	}
}

// The guard is only a guard if boot calls it.
func TestRunServerCallsTheTrustBackfillCheck(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "trustBackfillCheck(cfg, sqlStore)") {
		t.Error("runServer does not call trustBackfillCheck")
	}
}

func TestBootRefusesAMarkerFromAnotherCutoff(t *testing.T) {
	run := &trust.BackfillRun{FinishedAt: time.Now(), Cutoff: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	if err := trustBackfillCheck(trustCfg(t, "true"), platformWithMarker{run: run}); !errors.Is(err, trust.ErrBackfillCutoffMismatch) {
		t.Errorf("got %v, want a cutoff mismatch", err)
	}
}

// A database that never answers must stop the boot, not hang it.
func TestBootGivesUpWhenTheMarkerNeverAnswers(t *testing.T) {
	previous := trustBackfillTimeout
	trustBackfillTimeout = 20 * time.Millisecond
	defer func() { trustBackfillTimeout = previous }()
	start := time.Now()
	err := trustBackfillCheck(trustCfg(t, "true"), platformThatHangs{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got %v, want a deadline error", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %s", time.Since(start))
	}
}
