package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// singleHostProvisioner stands in for the Docker provisioners: no rollback of
// its own, images it can fetch ahead of the build.
type singleHostProvisioner struct {
	*provisioner.PostgreSQLProvisioner
	pull          func(context.Context) error
	provisionErr  error
	deprovisions  atomic.Int32
	deprovisionOn error
}

func (p *singleHostProvisioner) PrepareImages(ctx context.Context, _ domain.ProvisioningRequest) error {
	if p.pull == nil {
		return nil
	}
	return p.pull(ctx)
}

func (p *singleHostProvisioner) Provision(ctx context.Context, _ domain.ProvisioningRequest, _ config.TierConfig, _ provisioner.StageCallback) (*provisioner.ProvisioningResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.provisionErr != nil {
		return nil, p.provisionErr
	}
	return &provisioner.ProvisioningResult{Host: "c", Port: 5432, DatabaseName: "app", Username: "app", Password: "x", Namespace: "c"}, nil
}

func (p *singleHostProvisioner) Deprovision(context.Context, string, string) error {
	p.deprovisions.Add(1)
	return p.deprovisionOn
}

// noRollback hides ProvisionWithRollback, as a Docker provisioner has none.
type noRollback struct{ *singleHostProvisioner }

func (noRollback) ProvisionWithRollback() {}

func singleHostService(t *testing.T, prov *singleHostProvisioner) (*ProvisioningService, *storage.FileSystemStore) {
	t.Helper()
	_, store, mock := setupProvisioningTest(t)
	prov.PostgreSQLProvisioner = provisioner.NewPostgreSQLProvisioner(k8s.NewMockClient(), "")
	svc := NewProvisioningService(store, provisioner.NewFactory(noRollback{prov}), mock)
	svc.SetOrgStore(testOrgs())
	svc.SetVault(newFakeVault())
	withBackupTarget(t, svc)
	return svc, store
}

// A slow first pull is not the build hanging.
func TestBuild_AnImagePullDoesNotCountAgainstTheDeadline(t *testing.T) {
	prov := &singleHostProvisioner{provisionErr: errors.New("stop here")}
	pulled := false
	prov.pull = func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
			pulled = true
			return nil
		}
	}
	svc, store := singleHostService(t, prov)
	svc.buildTimeout = 50 * time.Millisecond

	resp, err := svc.Provision(context.Background(), newProjectRequest())
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	row, _ := store.FindByProjectID(resp.ProjectID)
	if !pulled {
		t.Fatal("the images were not fetched ahead of the build")
	}
	if row.FailureReason != "stop here" {
		t.Fatalf("reason %q, want the build to get past the pull with its clock unspent", row.FailureReason)
	}
}

// A pull has a limit of its own, and a failed one fails the project by name.
func TestBuild_AHungImagePullEndsAtItsOwnLimit(t *testing.T) {
	prov := &singleHostProvisioner{}
	prov.pull = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	svc, store := singleHostService(t, prov)
	svc.imagePullTimeout = 50 * time.Millisecond

	var resp *domain.ProvisioningResponse
	within(t, 5*time.Second, "build", func() {
		var err error
		if resp, err = svc.Provision(context.Background(), newProjectRequest()); err != nil {
			t.Errorf("provision: %v", err)
		}
	})
	row, _ := store.FindByProjectID(resp.ProjectID)
	if row.Status != "FAILED" || row.FailureStep != "pull image" {
		t.Fatalf("status=%s step=%q reason=%q", row.Status, row.FailureStep, row.FailureReason)
	}
}

// A failed build on a single host removes what it started.
func TestBuild_AFailedSingleHostBuildRemovesItsContainer(t *testing.T) {
	prov := &singleHostProvisioner{provisionErr: errors.New("never healthy")}
	svc, store := singleHostService(t, prov)

	resp, err := svc.Provision(context.Background(), newProjectRequest())
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if prov.deprovisions.Load() != 1 {
		t.Fatalf("%d removals, want 1", prov.deprovisions.Load())
	}
	row, _ := store.FindByProjectID(resp.ProjectID)
	requireCleanRollback(t, row)
}

// And the sweep finishes a removal that failed.
func TestRetryFailedRollbacks_FinishesAFailedSingleHostRemoval(t *testing.T) {
	prov := &singleHostProvisioner{provisionErr: errors.New("never healthy"), deprovisionOn: errors.New("engine busy")}
	svc, store := singleHostService(t, prov)
	resp, err := svc.Provision(context.Background(), newProjectRequest())
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	row, _ := store.FindByProjectID(resp.ProjectID)
	row.DeploymentMode = domain.ModeDocker
	if err := store.Update(row); err != nil {
		t.Fatal(err)
	}
	requireFailedCleanupRecorded(t, row, removeFailedBuildCleanup(resp.ProjectID))

	prov.deprovisionOn = nil
	if finished := svc.RetryFailedRollbacks(context.Background()); len(finished) != 1 {
		t.Fatalf("finished %v", finished)
	}
	after, _ := store.FindByProjectID(resp.ProjectID)
	requireCleanRollback(t, after)
}
