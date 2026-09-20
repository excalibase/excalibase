package k8s

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
)

// EXC-409: the gateway container is added to a project's Postgres pod by a
// CNPG-I plugin, and a cluster asks for it by naming the plugin in
// spec.plugins. A project that did not ask for DocumentDB must name it
// nowhere — the plugin is then never called for that cluster and its pods are
// byte-for-byte what they were before any of this existed.

// clusterPlugins returns the built cluster's spec.plugins entries.
func clusterPlugins(t *testing.T, opts PostgreSQLClusterOpts) []interface{} {
	t.Helper()
	spec := BuildPostgreSQLCluster(opts).Object["spec"].(map[string]interface{})
	plugins, ok := spec["plugins"]
	if !ok {
		return nil
	}
	entries, ok := plugins.([]interface{})
	if !ok {
		t.Fatalf("spec.plugins is %T, want a list", plugins)
	}
	return entries
}

// documentDBClusterOpts is a DocumentDB project's cluster options.
func documentDBClusterOpts() PostgreSQLClusterOpts {
	return PostgreSQLClusterOpts{
		ProjectID:    "proj-doc100001",
		Namespace:    "org-a-proj-doc100001",
		Tier:         documentDBTier(),
		DatabaseName: "appdb",
		DocumentDB:   true,
		DocumentDBGatewayImage: "ghcr.io/example/gateway@sha256:" +
			"1111111111111111111111111111111111111111111111111111111111111111",
	}
}

// A DocumentDB project's cluster names the plugin, enabled.
func TestDocumentDBClusterRegistersTheSidecarInjector(t *testing.T) {
	entries := clusterPlugins(t, documentDBClusterOpts())
	if len(entries) != 1 {
		t.Fatalf("spec.plugins has %d entries, want 1: %v", len(entries), entries)
	}
	plugin := entries[0].(map[string]interface{})
	if plugin["name"] != config.DocumentDBPluginName {
		t.Errorf("plugin name: got %v, want %q", plugin["name"], config.DocumentDBPluginName)
	}
	if plugin["enabled"] != true {
		t.Errorf("plugin enabled: got %v, want true", plugin["enabled"])
	}
}

// The gateway image is named by the cluster rather than left to the plugin's
// built-in default, so what a tenant runs is recorded on the object that runs
// it and cannot change when the plugin is upgraded.
func TestDocumentDBClusterNamesTheGatewayImageItself(t *testing.T) {
	opts := documentDBClusterOpts()
	entries := clusterPlugins(t, opts)
	parameters := entries[0].(map[string]interface{})["parameters"].(map[string]interface{})

	if parameters["gatewayImage"] != opts.DocumentDBGatewayImage {
		t.Errorf("gatewayImage: got %v, want %q", parameters["gatewayImage"], opts.DocumentDBGatewayImage)
	}
}

// The Mongo side needs an identity of its own, and the plugin reads it from a
// Secret in the project's namespace. Naming the project's Secret keeps every
// tenant's credential inside that tenant's namespace.
func TestDocumentDBClusterNamesTheProjectsOwnCredentialSecret(t *testing.T) {
	opts := documentDBClusterOpts()
	entries := clusterPlugins(t, opts)
	parameters := entries[0].(map[string]interface{})["parameters"].(map[string]interface{})

	want := DocumentDBCredentialSecretName(opts.ProjectID)
	if parameters["documentDbCredentialSecret"] != want {
		t.Errorf("documentDbCredentialSecret: got %v, want %q",
			parameters["documentDbCredentialSecret"], want)
	}
	if want == "documentdb-credentials" {
		t.Error("the secret name is not scoped to the project")
	}
}

// The gateway terminates TLS itself and generates a self-signed certificate
// when given none. Pointing it at the cluster's own serving certificate means
// a Mongo client verifies against the same CA the endpoint API already hands
// out for Postgres, rather than a second, unverifiable one.
func TestDocumentDBClusterGivesTheGatewayTheClustersServingCertificate(t *testing.T) {
	opts := documentDBClusterOpts()
	entries := clusterPlugins(t, opts)
	parameters := entries[0].(map[string]interface{})["parameters"].(map[string]interface{})

	want := opts.ProjectID + "-postgres-server"
	if parameters["gatewayTLSSecret"] != want {
		t.Errorf("gatewayTLSSecret: got %v, want %q", parameters["gatewayTLSSecret"], want)
	}
}

// The whole point of the opt-in: an ordinary project's cluster carries no
// plugin reference at all, so CNPG never calls the injector for it and its
// pods gain no container.
func TestAnOrdinaryClusterRegistersNoPlugin(t *testing.T) {
	opts := documentDBClusterOpts()
	opts.DocumentDB = false

	if entries := clusterPlugins(t, opts); len(entries) != 0 {
		t.Errorf("a project without DocumentDB registers plugins: %v", entries)
	}
	spec := BuildPostgreSQLCluster(opts).Object["spec"].(map[string]interface{})
	if _, present := spec["plugins"]; present {
		t.Error("a project without DocumentDB has a spec.plugins key at all")
	}
}

// A DocumentDB project with no gateway image configured must not fall through
// to the plugin's default: the platform pins what its tenants run, and an
// unpinned image is a different decision from a pinned one.
func TestDocumentDBClusterWithNoConfiguredImageRegistersNoPlugin(t *testing.T) {
	opts := documentDBClusterOpts()
	opts.DocumentDBGatewayImage = ""

	if entries := clusterPlugins(t, opts); len(entries) != 0 {
		t.Errorf("a cluster with no pinned gateway image registered a plugin anyway: %v", entries)
	}
}
