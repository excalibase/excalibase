package provisioner

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// A retried teardown can find the namespace already on its way out. Kubernetes
// is removing everything in it, and provisioning's rights there went with its
// role binding, so the only step left is to watch it go.
func TestDeprovisionOfANamespaceAlreadyBeingDeletedOnlyWaits(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.TerminatingNamespaces = map[string]bool{"org1-proj": true}
	prov := NewPostgreSQLProvisioner(mock, "")
	prov.SetDeletionPoller(fastPoller(3))

	if err := prov.Deprovision(context.Background(), "org1-proj", "proj"); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	for _, call := range mock.Calls {
		for _, inside := range []string{"UninstallHelmChart", "DeleteCRD", "ForceDeleteClusterPods", "DeleteNamespace"} {
			if strings.HasPrefix(call, inside) {
				t.Errorf("acted inside a namespace already being deleted: %s", call)
			}
		}
	}
	if !slices.Contains(mock.Calls, "NamespaceExists:org1-proj") {
		t.Errorf("did not wait for the namespace to go: %v", mock.Calls)
	}
}

func TestDeprovisionStillTearsDownALiveNamespace(t *testing.T) {
	mock := k8s.NewMockClient()
	prov := NewPostgreSQLProvisioner(mock, "")
	prov.SetDeletionPoller(fastPoller(3))

	if err := prov.Deprovision(context.Background(), "org1-proj", "proj"); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if !slices.Contains(mock.Calls, "DeleteNamespace:org1-proj") {
		t.Errorf("namespace not deleted: %v", mock.Calls)
	}
}
