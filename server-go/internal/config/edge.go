package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// parseEdgePodPorts reads "8080,8443": the edge pods' own listener ports, behind the node's hostPorts.
func parseEdgePodPorts(raw string) ([]int, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	seen := map[int]bool{}
	var ports []int
	for _, part := range strings.Split(raw, ",") {
		port, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("%q is not a port", part)
		}
		if seen[port] {
			return nil, fmt.Errorf("port %d is given twice", port)
		}
		seen[port] = true
		ports = append(ports, port)
	}
	return ports, nil
}

func envEdgePodPorts(key string) []int {
	ports, err := parseEdgePodPorts(os.Getenv(key))
	if err != nil {
		log.Fatalf("%s: %v", key, err)
	}
	return ports
}

// EdgeConfigured: functions and apps reach public platform hosts only when the edge pods are named.
func (c AppConfig) EdgeConfigured() bool {
	return c.EdgeNamespace != "" || len(c.EdgePodLabels) > 0 || len(c.EdgePodPorts) > 0
}

// validateEdge: all three or none, since a namespace alone would open every pod in it.
func (c AppConfig) validateEdge() error {
	if !c.EdgeConfigured() {
		return nil
	}
	if problems := validation.IsDNS1123Label(c.EdgeNamespace); len(problems) > 0 {
		return fmt.Errorf("EDGE_NAMESPACE %q: %s", c.EdgeNamespace, strings.Join(problems, "; "))
	}
	if len(c.EdgePodLabels) == 0 {
		return fmt.Errorf("EDGE_POD_LABELS is required with EDGE_NAMESPACE: the edge pods' labels")
	}
	if len(c.EdgePodPorts) == 0 {
		return fmt.Errorf("EDGE_POD_PORTS is required with EDGE_NAMESPACE: the edge pods' listener ports")
	}
	return nil
}
