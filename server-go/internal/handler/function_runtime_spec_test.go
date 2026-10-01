package handler

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// The runtime is told where provisioning is, and carries only its project's
// derived token, never the master secret (EXC-518, SEC-C5).
func TestRuntimeSpecNamesProvisioningAndCarriesTheProjectToken(t *testing.T) {
	h := NewFunctionHandler(edgefn.NewFunctionStore(t.TempDir()), nil, nil, &inMemoryInstanceStore{}, nil, "")
	h.SetK8sClient(k8s.NewMockClient(), testDenoImage, "master")
	h.SetRuntimeProvisioningURL("http://provisioning.platform.svc:24005")

	spec := h.denoRuntimeSpecFor("proj_a")
	if spec.ProvisioningURL != "http://provisioning.platform.svc:24005" {
		t.Fatalf("ProvisioningURL = %q", spec.ProvisioningURL)
	}
	if spec.RuntimeSecret == "master" || spec.RuntimeSecret != edgefn.DeriveRuntimeSecret("master", "proj_a") {
		t.Fatal("the runtime must carry its project's derived token, not the master secret")
	}
}
