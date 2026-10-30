package config

import (
	"os"
	"strings"
)

// HostMetrics is where single-host monitoring reads (EXC-670).
type HostMetrics struct {
	// NetDev is the host's /proc/1/net/dev, bind-mounted read-only; empty
	// leaves the host's network to the containers' sum.
	NetDev string
	// Containers are the platform's long-running containers, by name.
	Containers []string
}

func loadHostMetrics() HostMetrics {
	settings := HostMetrics{NetDev: strings.TrimSpace(os.Getenv("HOST_METRICS_NET_DEV"))}
	for _, entry := range strings.Split(os.Getenv("HOST_METRICS_CONTAINERS"), ",") {
		if name := strings.TrimSpace(entry); name != "" {
			settings.Containers = append(settings.Containers, name)
		}
	}
	return settings
}
