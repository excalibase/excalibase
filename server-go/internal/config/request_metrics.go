package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// loadRequestMetricsTargets reads REQUEST_METRICS_TARGETS: the headless
// Services of graphql and auth whose pods serve per-project request counters
// on an internal port (EXC-656), as host:port.
func loadRequestMetricsTargets() []string {
	var targets []string
	for _, entry := range strings.Split(os.Getenv("REQUEST_METRICS_TARGETS"), ",") {
		if target := strings.TrimSpace(entry); target != "" {
			targets = append(targets, target)
		}
	}
	return targets
}

func validateRequestMetricsTargets(targets []string) error {
	for _, target := range targets {
		host, port, err := net.SplitHostPort(target)
		if err != nil || host == "" || strings.ContainsAny(host, "/:") {
			return fmt.Errorf("REQUEST_METRICS_TARGETS: %q is not host:port", target)
		}
		if number, err := strconv.Atoi(port); err != nil || number < 1 || number > 65535 {
			return fmt.Errorf("REQUEST_METRICS_TARGETS: %q has no valid port", target)
		}
	}
	return nil
}
