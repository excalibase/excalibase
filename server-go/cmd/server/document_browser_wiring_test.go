package main

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

func TestDocumentBrowserNeedsKubernetesOrAnEngineAndAVault(t *testing.T) {
	instances := fakestore.NewInstances()
	v, err := vault.NewWithStore(vault.NewMemoryStore())
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	if buildDocumentBrowser(nil, nil, v, instances) != nil {
		t.Error("mounted without Kubernetes or an engine")
	}
	if buildDocumentBrowser(k8s.NewMockClient(), nil, nil, instances) != nil {
		t.Error("mounted without a vault")
	}
	if buildDocumentBrowser(k8s.NewMockClient(), nil, v, instances) == nil {
		t.Error("not mounted with Kubernetes and a vault")
	}
	// EXC-576: a single host reaches its gateways through the engine.
	if buildDocumentBrowser(nil, &stubEngine{}, v, instances) == nil {
		t.Error("not mounted with an engine and a vault")
	}
}

// EXC-531: the export reaches a DocumentDB project's documents through the
// same gateway login as the browser, wherever the browser is mounted.
func TestSnapshotsExportDocumentsWhereverTheBrowserCanReachThem(t *testing.T) {
	instances := fakestore.NewInstances()
	if buildSnapshotService(instances, nil, t.TempDir(), nil).ExportsDocuments() {
		t.Error("documents exported without a gateway connector")
	}
	v, err := vault.NewWithStore(vault.NewMemoryStore())
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	connector := buildDocumentConnector(k8s.NewMockClient(), nil, v, instances)
	if !buildSnapshotService(instances, k8s.NewMockClient(), t.TempDir(), connector).ExportsDocuments() {
		t.Error("documents not exported with a gateway connector")
	}
}

// stubEngine stands for a container engine the wiring only holds.
type stubEngine struct{ provisioner.DockerClient }

// EXC-576: the gateway watch runs only where single-host DocumentDB does.
func TestTheGatewayWatchRunsOnlyForSingleHostDocumentDB(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		enabled bool
		want    bool
	}{{"docker", true, true}, {"docker", false, false}, {"k8s", true, false}, {"", true, false}} {
		cfg := config.AppConfig{ProvisionerMode: tc.mode, DocumentDBEnabled: tc.enabled}
		if got := documentDBGatewayWatchWanted(cfg); got != tc.want {
			t.Errorf("mode %q enabled %v: watch %v, want %v", tc.mode, tc.enabled, got, tc.want)
		}
	}
}

func TestSingleHostEndpointsAreDescribedOnlyOnASingleHost(t *testing.T) {
	instances := fakestore.NewInstances()
	if buildSingleHostEndpoints(config.AppConfig{ProvisionerMode: "k8s"}, instances, &stubEngine{}) != nil {
		t.Error("described on Kubernetes")
	}
	if buildSingleHostEndpoints(config.AppConfig{ProvisionerMode: "docker"}, instances, nil) != nil {
		t.Error("described without an engine")
	}
	if buildSingleHostEndpoints(config.AppConfig{ProvisionerMode: "docker"}, instances, &stubEngine{}) == nil {
		t.Error("not described on a single host")
	}
}
