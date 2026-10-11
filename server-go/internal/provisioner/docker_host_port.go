package provisioner

import (
	"context"
	"strconv"
	"strings"
)

// StartHoldingPort starts a container published on its project's held host
// port, waiting out a predecessor that has just freed it: rootless Podman's
// port forwarder can hold the port a moment longer.
func StartHoldingPort(ctx context.Context, docker DockerClient, poller Poller, container string) error {
	var last error
	err := poller.WaitUntilReady(ctx, "host port of "+container, func(ctx context.Context) (bool, error) {
		last = docker.StartContainer(ctx, container)
		if last != nil && strings.Contains(last.Error(), "address already in use") {
			return false, nil
		}
		return true, last
	})
	if err != nil && last != nil {
		return last
	}
	return err
}

// heldPort is the host port projectID publishes on: the one it holds, or ""
// (any free port) where no allocator is wired.
func heldPort(ctx context.Context, ports HostPortAllocator, projectID string) (string, error) {
	if ports == nil {
		return "", nil
	}
	port, err := ports.Allocate(ctx, projectID)
	if err != nil {
		return "", err
	}
	return strconv.Itoa(port), nil
}

// DocumentDBPorts are the host ports a DocumentDB project publishes Postgres
// and its gateway on; "" is any free port.
type DocumentDBPorts struct{ Postgres, Mongo string }

// HeldDocumentDBPorts are the ports projectID holds, taken the first time.
func HeldDocumentDBPorts(ctx context.Context, ports HostPortAllocator, projectID string) (DocumentDBPorts, error) {
	postgres, err := heldPort(ctx, ports, projectID)
	if err != nil || ports == nil {
		return DocumentDBPorts{Postgres: postgres}, err
	}
	mongo, err := ports.AllocateMongo(ctx, projectID)
	if err != nil {
		return DocumentDBPorts{}, err
	}
	return DocumentDBPorts{Postgres: postgres, Mongo: strconv.Itoa(mongo)}, nil
}
