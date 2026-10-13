package engineproxy

import (
	"encoding/json"
	"slices"
)

// The bus network (EXC-612) is where a project's CDC watcher reaches NATS.
// NATS has no per-client login (network isolation is its protection), so the
// network is admitted only for the operator's digest-pinned watcher image
// running its own entrypoint: tenant databases and apps, which would read
// every project's changes there, can never be created on it.

// checkCDCNetwork refuses a create that names the bus network unless it runs
// an admitted watcher image and publishes nothing.
func (p Policy) checkCDCNetwork(image string, entrypoint any, hostConfig map[string]any, endpoints map[string]json.RawMessage) error {
	if p.CDCNetwork == "" {
		return nil
	}
	_, joins := endpoints[p.CDCNetwork]
	if !joins {
		return nil
	}
	if !slices.Contains(p.CDCImages, image) {
		return refuse("image %q may not join the bus network %q", image, p.CDCNetwork)
	}
	if !isZero(hostConfig["PortBindings"]) {
		return refuse("a container on the bus network may not publish ports")
	}
	// The image's own program, not whatever else is in it.
	if !isZero(entrypoint) {
		return refuse("a container on the bus network keeps its image's entrypoint")
	}
	return nil
}

// cdcNetworkAdmitted reports the bus network once checkCDCNetwork vetted the create.
func (p Policy) cdcNetworkAdmitted(network, image string) bool {
	return p.CDCNetwork != "" && network == p.CDCNetwork && slices.Contains(p.CDCImages, image)
}
