package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/orgguard"
)

func suspendedOwnerGuard(svc *ProvisioningService, orgs ...string) {
	set := map[string]bool{}
	for _, id := range orgs {
		set[id] = true
	}
	svc.SetOrgGuard(orgguard.New(svc.store, func(context.Context) (map[string]bool, error) { return set, nil }))
}

func TestProvision_RefusedWhileTheOrgOwnerIsSuspended(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	suspendedOwnerGuard(svc, "org1")
	req := domain.ProvisioningRequest{PostgresVersion: "17", ProjectName: "new-one", OrgID: "org1", DBType: domain.PostgreSQL}

	if _, err := svc.Provision(context.Background(), req); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("Provision err = %v, want ErrOrgSuspended", err)
	}
	if _, err := svc.ProvisionInBackground(context.Background(), req); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("ProvisionInBackground err = %v, want ErrOrgSuspended", err)
	}
	noDatabase := req
	noDatabase.NoDatabase = true
	if _, err := svc.Provision(context.Background(), noDatabase); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("Provision without a database err = %v, want ErrOrgSuspended", err)
	}
	if all, _ := store.FindAll(); len(all) != 0 {
		t.Errorf("%d projects recorded, want none", len(all))
	}
}

func TestProvision_AnOrgWithoutASuspensionIsUnaffected(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	suspendedOwnerGuard(svc, "another-org")

	req := domain.ProvisioningRequest{PostgresVersion: "17", ProjectName: "new-one", OrgID: "org1", DBType: domain.PostgreSQL}
	if _, err := svc.Provision(context.Background(), req); err != nil {
		t.Fatalf("Provision: %v", err)
	}
}

func TestAddDatabase_RefusedWhileTheOrgOwnerIsSuspended(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	store.Create(&domain.DatabaseInstance{ProjectID: "p1", OrgID: "org1", Status: "ACTIVE", NoDatabase: true})
	suspendedOwnerGuard(svc, "org1")
	req := domain.ProvisioningRequest{PostgresVersion: "17", DBType: domain.PostgreSQL}

	if _, err := svc.AddDatabase(context.Background(), "p1", req); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("AddDatabase err = %v, want ErrOrgSuspended", err)
	}
	if _, err := svc.AddDatabaseInBackground(context.Background(), "p1", req); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("AddDatabaseInBackground err = %v, want ErrOrgSuspended", err)
	}
}

func TestUpgradeVersion_RefusedWhileTheOrgOwnerIsSuspended(t *testing.T) {
	svc, _, mock := setupOpsTest(t)
	mock.ApplyCRD(context.Background(), k8s.CNPGClusterGVR, testOpsDBNS, k8s.BuildPostgreSQLCluster(k8s.PostgreSQLClusterOpts{
		ProjectID: testOpsDB, Namespace: testOpsDBNS,
		Tier: config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5"},
	}))
	suspendedOwnerGuard(svc, "org1")

	if err := svc.UpgradeVersion(context.Background(), testOpsDB, "17"); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("UpgradeVersion err = %v, want ErrOrgSuspended", err)
	}
	got, _ := mock.GetCRD(context.Background(), k8s.CNPGClusterGVR, testOpsDBNS, testOpsDBPostgres)
	want, _ := config.PostgresImage("17")
	if spec := got.Object["spec"].(map[string]interface{}); spec["imageName"] == want {
		t.Error("a refused upgrade still re-pinned the cluster image")
	}
}
