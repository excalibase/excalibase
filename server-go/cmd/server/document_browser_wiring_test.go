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
