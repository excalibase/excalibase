package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

type panickingCluster struct{ *k8s.MockClient }

func (c *panickingCluster) ExecInPod(context.Context, string, string, string, []string) (string, error) {
	panic("secret-looking internal detail")
}

func (c *panickingCluster) ExecInPodStdin(context.Context, string, string, string, []string, string) (string, error) {
	panic("secret-looking internal detail")
}

// A panic in a build must not leave the row PROVISIONING with the cluster's
// resources behind it: the build fails, rolls back, and says nothing internal.
func TestBuild_APanicFailsTheProjectAndRollsBack(t *testing.T) {
	_, store, mock := setupProvisioningTest(t)
	cluster := &panickingCluster{MockClient: mock}
	svc := NewProvisioningService(store, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(cluster, "")), cluster)
	svc.SetOrgStore(testOrgs())
	svc.SetVault(newFakeVault())
	withBackupTarget(t, svc)

	var resp *domain.ProvisioningResponse
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("the panic reached the caller: %v", r)
			}
		}()
		var err error
		if resp, err = svc.Provision(context.Background(), newProjectRequest()); err != nil {
			t.Fatalf("provision: %v", err)
		}
	}()

	row, _ := store.FindByProjectID(resp.ProjectID)
	if row.Status != "FAILED" {
		t.Fatalf("status %s, want FAILED", row.Status)
	}
	if strings.Contains(row.FailureReason, "secret-looking") {
		t.Errorf("the reason exposes the panic: %q", row.FailureReason)
	}
	if row.FailureReason == "" {
		t.Error("no reason recorded")
	}
	if mock.Namespaces[row.Namespace] {
		t.Errorf("namespace %s survived the rollback", row.Namespace)
	}
}

// A panic in anything started in the background must not take the whole
// control plane with it.
func TestStartInBackground_SurvivesAPanic(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	reached := make(chan struct{})
	svc.startInBackground(func() {
		defer close(reached)
		panic("boom")
	})
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the background work never ran")
	}
	time.Sleep(50 * time.Millisecond) // the recover runs after the deferred close
}

// The same protection holds for a runner a test or a caller supplied.
func TestStartInBackground_ProtectsASuppliedRunner(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	svc.SetBackgroundRunner(func(f func()) { f() })
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("the panic escaped: %v", r)
		}
	}()
	svc.startInBackground(func() { panic("boom") })
}
