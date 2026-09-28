package apphost

import (
	"errors"
	"fmt"
)

// Internal ports (EXC-525): raw TCP ports an app opens to its own project's
// apps only, never to the edge. The range starts above the well-known ports,
// so the Service's HTTP port 80 can never be taken.
const (
	MaxInternalPorts = 8
	MinInternalPort  = 1024
	MaxInternalPort  = 65535
	ProtocolTCP      = "TCP"
)

// InternalPort is one declared port. Protocol is required and only TCP is offered.
type InternalPort struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
}

func validateInternalPorts(httpPort int, ports []InternalPort) error {
	if len(ports) > MaxInternalPorts {
		return fmt.Errorf("an app may declare at most %d internal ports", MaxInternalPorts)
	}
	seen := make(map[int]bool, len(ports))
	for _, port := range ports {
		if port.Protocol != ProtocolTCP {
			return fmt.Errorf("internal port %d: protocol must be %q", port.Port, ProtocolTCP)
		}
		if port.Port < MinInternalPort || port.Port > MaxInternalPort {
			return fmt.Errorf("internal port %d: must be between %d and %d", port.Port, MinInternalPort, MaxInternalPort)
		}
		if port.Port == httpPort {
			return errors.New("an internal port cannot be the app's HTTP port")
		}
		if seen[port.Port] {
			return fmt.Errorf("internal port %d is declared twice", port.Port)
		}
		seen[port.Port] = true
	}
	return nil
}

func cloneInternalPorts(ports []InternalPort) []InternalPort {
	if ports == nil {
		return nil
	}
	return append([]InternalPort{}, ports...)
}
