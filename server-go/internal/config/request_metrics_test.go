package config

import (
	"reflect"
	"testing"
)

func TestLoad_RequestMetricsTargetsAreTheListedHostPorts(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.test")
	t.Setenv("REQUEST_METRICS_TARGETS", " graphql-request-metrics.excalibase.svc:9464, ,auth-request-metrics.excalibase.svc:9465 ")
	want := []string{"graphql-request-metrics.excalibase.svc:9464", "auth-request-metrics.excalibase.svc:9465"}
	if got := Load().RequestMetricsTargets; !reflect.DeepEqual(got, want) {
		t.Errorf("targets: %v", got)
	}
}

func TestLoad_NoRequestMetricsTargetsByDefault(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.test")
	t.Setenv("REQUEST_METRICS_TARGETS", "")
	if got := Load().RequestMetricsTargets; len(got) != 0 {
		t.Errorf("targets: %v", got)
	}
}

func TestValidate_ARequestMetricsTargetNeedsAHostAndPort(t *testing.T) {
	for _, target := range []string{"graphql", "http://graphql:9464", ":9464", "graphql:port"} {
		if err := validateRequestMetricsTargets([]string{target}); err == nil {
			t.Errorf("%q accepted", target)
		}
	}
	if err := validateRequestMetricsTargets([]string{"graphql-request-metrics.excalibase.svc.cluster.local:9464"}); err != nil {
		t.Errorf("a service name and port: %v", err)
	}
}
