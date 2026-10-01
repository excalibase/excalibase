package main

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

func TestDocumentBrowserNeedsKubernetesAndAVault(t *testing.T) {
	instances := fakestore.NewInstances()
	if buildDocumentBrowser(nil, nil, instances) != nil {
		t.Error("mounted without Kubernetes")
	}
	v, err := vault.NewWithStore(vault.NewMemoryStore())
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	if buildDocumentBrowser(k8s.NewMockClient(), v, instances) == nil {
		t.Error("not mounted with Kubernetes and a vault")
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
	connector := buildDocumentConnector(k8s.NewMockClient(), v, instances)
	if !buildSnapshotService(instances, k8s.NewMockClient(), t.TempDir(), connector).ExportsDocuments() {
		t.Error("documents not exported with a gateway connector")
	}
}
