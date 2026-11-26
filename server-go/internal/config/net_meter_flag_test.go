package config

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
)

// EXC-844: network metering ships dark and runs on Kubernetes and on a single host alike.
func TestLoad_NetMeterIsOffUntilItsVariableSaysTrue_OnEveryMode(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.test")
	for _, mode := range []string{"", "docker"} {
		t.Setenv("PROVISIONER_MODE", mode)
		for value, want := range map[string]bool{"": false, "yes": false, "false": false, "true": true} {
			t.Setenv("FEATURE_NETMETER", value)
			if got := Load().Features().Enabled(context.Background(), features.NetMeter); got != want {
				t.Errorf("mode %q FEATURE_NETMETER=%q: enabled=%v, want %v", mode, value, got, want)
			}
		}
	}
}

func TestLoad_NetMeterKubeletSettingsHaveInClusterDefaults(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.test")
	for _, name := range []string{"NETMETER_KUBELET_CA_FILE", "NETMETER_KUBELET_TOKEN_FILE", "NETMETER_KUBELET_PORT"} {
		t.Setenv(name, "")
	}
	got := Load().NetMeter
	if got.KubeletCAFile != "/etc/excalibase/kubelet-ca/ca.crt" || got.KubeletTokenFile != "/var/run/secrets/kubernetes.io/serviceaccount/token" || got.KubeletPort != "10250" {
		t.Errorf("defaults = %+v", got)
	}
	t.Setenv("NETMETER_KUBELET_CA_FILE", " /x/ca.crt ")
	t.Setenv("NETMETER_KUBELET_PORT", "10251")
	got = Load().NetMeter
	if got.KubeletCAFile != "/x/ca.crt" || got.KubeletPort != "10251" {
		t.Errorf("overrides = %+v", got)
	}
}
