package apphost

import (
	"errors"
	"fmt"
)

// Internal ports (EXC-525): raw TCP ports an app opens to its own project's
// apps only, never to the edge. The range starts above the well-known ports,
// so the Service's HTTP port 80 can never be taken.
const (
	// ServiceHTTPPort is where a public app's Service serves its HTTP port inside the project.
	ServiceHTTPPort  = 80
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

// validateExposure: a public web app has an HTTP port; an internal service has
// none, no HTTP health path, and at least one internal port to be reached on.
func (a *App) validateExposure() error {
	if !a.Internal {
		if a.Port < 1 || a.Port > 65535 {
			return errors.New("port must be between 1 and 65535")
		}
		return nil
	}
	switch {
	case a.Port != 0:
		return errors.New("an internal service has no HTTP port; declare its internal ports instead")
	case a.HealthCheckPath != "":
		return errors.New("an internal service has no HTTP health check; it is checked on its first internal port")
	case len(a.InternalPorts) == 0:
		return errors.New("an internal service needs at least one internal port")
	}
	return nil
}
