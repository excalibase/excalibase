package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// midProvisionProvisioner runs a callback partway through the provision, so
// a test can land a DELETE while the pipeline is still creating resources.
type midProvisionProvisioner struct {
	*provisioner.PostgreSQLProvisioner
	during func()
	fired  bool
	// abortedCtx records whether the provision context was cancelled by the
	// time the pipeline handed back — the signal that the pipeline was told
	// to stop rather than being left to run to completion.
	abortedCtx bool
}

func (p *midProvisionProvisioner) Provision(ctx context.Context, req domain.ProvisioningRequest,
	tier config.TierConfig, cb provisioner.StageCallback) (*provisioner.ProvisioningResult, error) {
	if !p.fired {
		p.fired = true
		p.during()
	}
	result, err := p.PostgreSQLProvisioner.Provision(ctx, req, tier, cb)
	p.abortedCtx = ctx.Err() != nil
	return result, err
}

func (p *midProvisionProvisioner) ProvisionWithRollback(ctx context.Context, req domain.ProvisioningRequest,
	tier config.TierConfig, pc *provisioner.ProvisionContext) (*provisioner.ProvisioningResult, error) {
	if !p.fired {
		p.fired = true
		p.during()
	}
	result, err := p.PostgreSQLProvisioner.ProvisionWithRollback(ctx, req, tier, pc)
	p.abortedCtx = ctx.Err() != nil
	return result, err
}

func provisionAgainstDeletion(t *testing.T, during func(*ProvisioningService)) (*ProvisioningService, *storage.FileSystemStore, *domain.ProvisioningResponse, error) {
	_, store, resp, err, _ := provisionAgainstDeletionWithProvisioner(t, during)
	return nil, store, resp, err
}

func provisionAgainstDeletionWithProvisioner(t *testing.T, during func(*ProvisioningService)) (*ProvisioningService, *storage.FileSystemStore, *domain.ProvisioningResponse, error, *midProvisionProvisioner) {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	mock.WildcardPodReady = true
	mock.WildcardSecret = map[string][]byte{
		"username": []byte("app"), "password": []byte("testpassword123"), "dbname": []byte("app"),
	}
	inner := provisioner.NewPostgreSQLProvisioner(mock, "")
	inner.SetDeletionPoller(fastPoller(3))

	var svc *ProvisioningService
	wrapped := &midProvisionProvisioner{PostgreSQLProvisioner: inner, during: func() { during(svc) }}
	svc = NewProvisioningService(store, provisioner.NewFactory(wrapped), mock)
	svc.SetVault(newFakeVault())

	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "race-db", OrgID: "org1", DBType: domain.PostgreSQL, Tier: domain.Free,
	})
	return svc, store, resp, err, wrapped
}

// A DELETE arriving while the pipeline is still building the project is
// refused: the build is creating resources the teardown would not see.
func TestDeleteIsRefusedWhileAProvisionIsRunning(t *testing.T) {
	var deleteErr error
	_, store, resp, err := provisionAgainstDeletion(t, func(svc *ProvisioningService) {
		all, _ := svc.GetAllInstances()
		deleteErr = svc.Deprovision(context.Background(), all[0].ProjectID)
	})
	if err != nil {
		t.Fatalf(testProvisionFmt, err)
	}
	if !errors.Is(deleteErr, storage.ErrProjectBusy) {
		t.Fatalf("DELETE during a provision = %v, want ErrProjectBusy", deleteErr)
	}
	if resp.Status != "ACTIVE" {
		t.Fatalf("the provision must finish normally, got %s (%s)", resp.Status, resp.FailureReason)
	}
	if inst, _ := store.FindByProjectID(resp.ProjectID); inst == nil || inst.Status != "ACTIVE" {
		t.Fatalf("row = %v, want an ACTIVE project", inst)
	}
}

// When the door does close under a running provision — the stale-build or
// admin force path — the pipeline must stop and undo what it created rather
// than register a project whose record is about to disappear.
func TestProvisionAbortsWhenTheDoorClosesUnderIt(t *testing.T) {
	vault := newFakeVault()
	var claimed string
	svc, store, resp, err, prov := provisionAgainstDeletionWithProvisioner(t, func(svc *ProvisioningService) {
		svc.SetVault(vault)
		all, _ := svc.GetAllInstances()
		claimed = all[0].ProjectID
		// Stand in for the stale-build / admin force path: the claim is
		// taken while the pipeline is still running.
		if _, berr := forceClaim(svc, claimed); berr != nil {
			t.Errorf("force claim: %v", berr)
		}
	})
	if err != nil {
		t.Fatalf(testProvisionFmt, err)
	}
	if resp.Status != "FAILED" {
		t.Fatalf("the provision must abort, got %s", resp.Status)
	}
	if inst, _ := store.FindByProjectID(claimed); inst == nil || inst.Status != string(domain.StatusDeleting) {
		t.Fatalf("row = %v, want it still owned by the teardown", inst)
	}
	for path := range vault.data {
		t.Errorf("the aborted provision left a vault secret behind: %s", path)
	}
	if !prov.abortedCtx {
		t.Error("the pipeline was left running to completion; it must be told to stop as soon as the door closes")
	}
	_ = svc
}

// forceClaim takes the deletion claim directly, the way the stale-build rule
// and the admin force-drop path reach a row the normal guard would refuse.
func forceClaim(svc *ProvisioningService, projectID string) (bool, error) {
	inst, err := svc.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return false, errors.New("project not found")
	}
	claimed := inst.Clone()
	claimed.Status = "ACTIVE" // bypass the busy guard, as the stale rule does
	if err := svc.store.Update(claimed); err != nil {
		return false, err
	}
	return svc.store.BeginDeletion(projectID, nil)
}
