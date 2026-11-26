package main

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/behaviour"
	"github.com/excalibase/provisioning-poc/internal/netmeter"
)

type fullReviewStore struct{}

func (fullReviewStore) RecentNetMinutes(context.Context, time.Time) ([]netmeter.Sample, error) {
	return nil, nil
}
func (fullReviewStore) RecentNetHours(context.Context, time.Time) ([]netmeter.Hourly, error) {
	return nil, nil
}
func (fullReviewStore) NetVolumes(context.Context, time.Time) ([]behaviour.AppVolume, error) {
	return nil, nil
}
func (fullReviewStore) ReviewDirectory(context.Context) ([]behaviour.Subject, map[string]behaviour.Subject, error) {
	return nil, nil, nil
}
func (fullReviewStore) RecordFinding(context.Context, behaviour.Finding, time.Time, behaviour.Rules) (behaviour.Outcome, error) {
	return behaviour.OutcomeOpened, nil
}
func (fullReviewStore) PruneReviews(context.Context, time.Time, int) error { return nil }

func TestBehaviourSampler_OffWhileTheFlagIsOff(t *testing.T) {
	t.Setenv("FEATURE_BEHAVIOURREVIEW", "")
	if behaviourSampler(netMeterConfig(t, "", ""), fullReviewStore{}) != nil {
		t.Fatal("flag off: nothing is built and nothing is read")
	}
}

func TestBehaviourSampler_RunsOnKubernetesAndSingleHostWithoutOtherFlags(t *testing.T) {
	for _, mode := range []string{"", "docker"} {
		t.Setenv("FEATURE_BEHAVIOURREVIEW", "true")
		cfg := netMeterConfig(t, mode, "")
		if behaviourSampler(cfg, fullReviewStore{}) == nil {
			t.Errorf("mode %q: the detector needs only the flag and the tables", mode)
		}
		if !metricsLoopRuns(cfg, nil, nil) && mode == "docker" {
			t.Error("on a single host the loop must run for the detector alone")
		}
	}
}

func TestBehaviourSampler_NeedsTheStoreToKeepTheTables(t *testing.T) {
	t.Setenv("FEATURE_BEHAVIOURREVIEW", "true")
	if behaviourSampler(netMeterConfig(t, "", ""), struct{}{}) != nil {
		t.Fatal("a store without the review tables: off, and it says so")
	}
}
