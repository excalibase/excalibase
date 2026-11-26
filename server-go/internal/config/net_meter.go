package config

import (
	"os"
	"strings"
)

// NetMeter is where the Kubernetes network meter reads the kubelet (EXC-844).
// The CA is the one that signs the kubelet's serving certificate; there is no
// way to skip checking it.
type NetMeter struct {
	KubeletCAFile    string
	KubeletTokenFile string
	KubeletPort      string
}

func loadNetMeter() NetMeter {
	return NetMeter{
		KubeletCAFile:    envOrDefault("NETMETER_KUBELET_CA_FILE", "/etc/excalibase/kubelet-ca/ca.crt"),
		KubeletTokenFile: envOrDefault("NETMETER_KUBELET_TOKEN_FILE", "/var/run/secrets/kubernetes.io/serviceaccount/token"),
		KubeletPort:      envOrDefault("NETMETER_KUBELET_PORT", "10250"),
	}
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
