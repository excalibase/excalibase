package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

func sizedConfig() config.AppConfig {
	return config.AppConfig{
		ProvisionerMode: "k8s", TenantStorageClass: "excalibase-tenant", TenantStorageRequireSized: true,
		TenantSizedProvisioners: []string{"local.csi.openebs.io"}, StorageBudgetPercent: 80,
		StorageLVMNamespace: "openebs", StorageLVMVolumeGroup: "excalibase-tenants",
	}
}

func TestBuildStorageBudget(t *testing.T) {
	kube := k8s.NewMockClient()
	kube.VolumeGroupBytes = 100 << 30
	budget, err := buildStorageBudget(sizedConfig(), kube)
	if err != nil || budget == nil {
		t.Fatalf("budget %v, %v", budget, err)
	}
	report, err := budget.Report(context.Background())
	if err != nil || report.BudgetBytes != 80<<30 {
		t.Fatalf("report %+v, %v", report, err)
	}
	fixed := config.AppConfig{StorageBudgetPercent: 80, StorageNodeCapacity: "10Gi"}
	budget, _ = buildStorageBudget(fixed, kube)
	if report, _ := budget.Report(context.Background()); report.CapacityBytes != 10<<30 {
		t.Fatalf("fixed capacity report %+v", report)
	}
	if budget, err := buildStorageBudget(config.AppConfig{}, kube); budget != nil || err != nil {
		t.Fatalf("no capacity configured: %v %v, want no budget", budget, err)
	}
}

// Production refuses to start without sized tenant storage and a readable budget.
func TestVerifyTenantStorage(t *testing.T) {
	kube := k8s.NewMockClient()
	kube.VolumeGroupBytes = 100 << 30
	budget, _ := buildStorageBudget(sizedConfig(), kube)
	if err := verifyTenantStorage(context.Background(), sizedConfig(), kube, budget); err != nil {
		t.Fatalf("sized storage refused: %v", err)
	}
	kube.SizedClassErr = k8s.ErrStorageNotSized
	if err := verifyTenantStorage(context.Background(), sizedConfig(), kube, budget); !errors.Is(err, k8s.ErrStorageNotSized) {
		t.Fatalf("err = %v, want ErrStorageNotSized", err)
	}
	kube.SizedClassErr, kube.VolumeGroupBytes = nil, 0
	if err := verifyTenantStorage(context.Background(), sizedConfig(), kube, budget); err == nil || !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("err = %v, want the unreadable capacity refused", err)
	}
	if err := verifyTenantStorage(context.Background(), sizedConfig(), nil, budget); err == nil {
		t.Fatal("no Kubernetes client must be refused in production")
	}
	if err := verifyTenantStorage(context.Background(), config.AppConfig{}, nil, nil); err != nil {
		t.Fatalf("a test install needs nothing: %v", err)
	}
}
