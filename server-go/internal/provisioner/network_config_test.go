package provisioner

import "testing"

// In docker mode, tenant DB creds are stored as containerName:5432, which only
// resolves when the container and its consumers (graphql, the schema handler)
// share a user-defined network — the default bridge has no name resolution.
// So a configured network must be attached at container-create time.
func TestNetworkingConfig(t *testing.T) {
	// No network configured → no endpoints (default bridge; back-compat).
	nc := networkingConfig("")
	if len(nc.EndpointsConfig) != 0 {
		t.Errorf("empty network must yield no endpoints, got %v", nc.EndpointsConfig)
	}
	// Configured network → the container joins it.
	nc = networkingConfig("excalibase")
	if _, ok := nc.EndpointsConfig["excalibase"]; !ok {
		t.Errorf("container must join the configured network, got %v", nc.EndpointsConfig)
	}
	if len(nc.EndpointsConfig) != 1 {
		t.Errorf("exactly one endpoint expected, got %d", len(nc.EndpointsConfig))
	}
}
