package provisioner

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// Teardown destroys the pods anyway, so it does not wait out a sidecar that ignores SIGTERM.
func TestDeprovisionForceDeletesInstancePodsBeforeTheNamespace(t *testing.T) {
	mock := k8s.NewMockClient()
	prov := NewPostgreSQLProvisioner(mock, "")
	prov.SetDeletionPoller(fastPoller(3))

	if err := prov.Deprovision(context.Background(), "org1-proj", "proj"); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	force := slices.Index(mock.Calls, "ForceDeleteClusterPods:org1-proj/proj-postgres")
	cluster := slices.Index(mock.Calls, "DeleteCRD:org1-proj/proj-postgres")
	namespace := slices.Index(mock.Calls, "DeleteNamespace:org1-proj")
	if force < 0 || force < cluster || force > namespace {
		t.Errorf("order cluster=%d force=%d namespace=%d: %v", cluster, force, namespace, mock.Calls)
	}
}

func TestDeprovisionFailsWhenInstancePodsCannotBeRemoved(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.ForceDeletePodsError = errors.New("refused")
	prov := NewPostgreSQLProvisioner(mock, "")
	prov.SetDeletionPoller(fastPoller(3))

	if err := prov.Deprovision(context.Background(), "org1-proj", "proj"); !errors.Is(err, mock.ForceDeletePodsError) {
		t.Fatalf("Deprovision: got %v", err)
	}
	if slices.Contains(mock.Calls, "DeleteNamespace:org1-proj") {
		t.Error("the namespace was deleted although the instance pods were not")
	}
}
