package service

import (
	"context"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// goneCaller is a cluster whose first readiness poll finds the caller gone:
// the request's context is cancelled, as a closed connection cancels it. Every
// later poll finds the pod ready.
type goneCaller struct {
	*k8s.MockClient
	hangUp context.CancelFunc
	once   sync.Once
}

func (c *goneCaller) IsPodReady(ctx context.Context, namespace, name string) (bool, error) {
	first := false
	c.once.Do(func() { first = true })
	if first {
		c.hangUp()
		return false, nil
	}
	return c.MockClient.IsPodReady(ctx, namespace, name)
}

// A caller that waits for the create and then goes away must not take the
// project down with it: the platform builds it as it would for a caller that
// asked not to wait, and the project ends ACTIVE, not FAILED holding the
// organisation's slot with its half-built database removed.
func TestProvision_ACallerWhoHangsUpMidBuildDoesNotCancelTheBuild(t *testing.T) {
	_, store, mock := setupProvisioningTest(t)
	request, hangUp := context.WithCancel(context.Background())
	defer hangUp()
	client := &goneCaller{MockClient: mock, hangUp: hangUp}
	svc := NewProvisioningService(store, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(client, "")), client)
	svc.SetOrgStore(testOrgs())
	svc.SetVault(newFakeVault())
	withBackupTarget(t, svc)

	resp, err := svc.Provision(request, domain.ProvisioningRequest{
		ProjectName: "hangs up", OrgID: "org1", OwnerID: testUser1, DBType: domain.PostgreSQL, PostgresVersion: "17",
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	row, _ := store.FindByProjectID(resp.ProjectID)
	if row.Status != string(domain.StatusActive) {
		t.Fatalf("after the caller hung up the project is %s (%s at %s), want ACTIVE",
			row.Status, row.FailureReason, row.FailureStage)
	}
}
