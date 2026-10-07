package config

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

func TestLoadEdge(t *testing.T) {
	t.Setenv("EDGE_NAMESPACE", "haproxy-controller")
	t.Setenv("EDGE_POD_LABELS", "app.kubernetes.io/name=kubernetes-ingress")
	t.Setenv("EDGE_POD_PORTS", "8080, 8443")
	cfg := Load()
	if cfg.EdgeNamespace != "haproxy-controller" {
		t.Errorf("EdgeNamespace = %q", cfg.EdgeNamespace)
	}
	if !maps.Equal(cfg.EdgePodLabels, map[string]string{"app.kubernetes.io/name": "kubernetes-ingress"}) {
		t.Errorf("EdgePodLabels = %v", cfg.EdgePodLabels)
	}
	if !slices.Equal(cfg.EdgePodPorts, []int{8080, 8443}) {
		t.Errorf("EdgePodPorts = %v", cfg.EdgePodPorts)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a complete edge must pass: %v", err)
	}
}

func TestParseEdgePodPorts(t *testing.T) {
	if got, err := parseEdgePodPorts(" "); err != nil || got != nil {
		t.Errorf("empty: got %v, %v", got, err)
	}
	for _, raw := range []string{"http", "0", "65536", "-1", "8080,8080", "8080,,8443", "80-90"} {
		if _, err := parseEdgePodPorts(raw); err == nil {
			t.Errorf("%q must be refused", raw)
		}
	}
}

func edgeConfig() AppConfig {
	return AppConfig{
		ProvisionerMode: "k8s", DeploymentMode: "cloud",
		EdgeNamespace: "haproxy-controller", EdgePodLabels: map[string]string{"app.kubernetes.io/name": "kubernetes-ingress"},
		EdgePodPorts: []int{8080, 8443},
	}
}

// The edge is all three settings or none: a namespace alone would open every pod in it.
func TestValidateEdgeIsAllOrNothing(t *testing.T) {
	if err := (AppConfig{ProvisionerMode: "k8s", DeploymentMode: "cloud"}).Validate(); err != nil {
		t.Fatalf("no edge at all must pass: %v", err)
	}
	cases := map[string]struct {
		mutate func(*AppConfig)
		want   string
	}{
		"ports without namespace": {func(c *AppConfig) { c.EdgeNamespace = "" }, "EDGE_NAMESPACE"},
		"bad namespace":           {func(c *AppConfig) { c.EdgeNamespace = "Not_A_Namespace" }, "EDGE_NAMESPACE"},
		"no pod labels":           {func(c *AppConfig) { c.EdgePodLabels = nil }, "EDGE_POD_LABELS"},
		"namespace without ports": {func(c *AppConfig) { c.EdgePodPorts = nil }, "EDGE_POD_PORTS"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := edgeConfig()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error naming %s, got %v", tc.want, err)
			}
		})
	}
}
