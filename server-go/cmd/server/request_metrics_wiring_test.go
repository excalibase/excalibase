package main

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

// emptyRequestMetrics is a request_metrics table with nothing in it.
type emptyRequestMetrics struct{}

func (emptyRequestMetrics) AppendRequestMetrics(context.Context, []domain.RequestMetricsRow) error {
	return nil
}

func (emptyRequestMetrics) RequestMetricsSince(context.Context, string, time.Time) ([]domain.RequestMetricsRow, error) {
	return nil, nil
}

func (emptyRequestMetrics) PruneRequestMetricsBefore(context.Context, time.Time) error { return nil }

// EXC-656: nothing is scraped unless the feature is on and targets are set.
func TestRequestSamplingStartsOnlyWithItsFeatureAndTargets(t *testing.T) {
	svc := service.NewRequestMetricsService(nil, emptyRequestMetrics{}, fakestore.NewInstances())
	targets := []string{"graphql-request-metrics:9464"}
	cases := []struct {
		name string
		cfg  config.AppConfig
		want bool
	}{
		{"off", config.AppConfig{RequestMetricsTargets: targets}, false},
		{"on without targets", config.AppConfig{DarkFeatures: []features.Feature{features.RequestMetrics}}, false},
		{"on with targets", config.AppConfig{DarkFeatures: []features.Feature{features.RequestMetrics}, RequestMetricsTargets: targets}, true},
	}
	for _, tc := range cases {
		if got := requestSampling(tc.cfg, svc) != nil; got != tc.want {
			t.Errorf("%s: sampling=%v, want %v", tc.name, got, tc.want)
		}
	}
	if requestSampling(config.AppConfig{DarkFeatures: []features.Feature{features.RequestMetrics}, RequestMetricsTargets: targets}, nil) != nil {
		t.Error("no table, no sampling")
	}
}
