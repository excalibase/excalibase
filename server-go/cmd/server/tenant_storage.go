package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/metrics"
	"github.com/excalibase/provisioning-poc/internal/storagebudget"
)

const (
	appDiskJobTimeout     = 10 * time.Minute
	storageMetricsRefresh = time.Minute
)

// buildStorageBudget is nil when the install does not know its storage: a
// test cluster, where nothing is held to a budget.
func buildStorageBudget(cfg config.AppConfig, kube k8s.KubeClient) (*storagebudget.Budget, error) {
	if !cfg.StorageBudgetEnabled() || kube == nil {
		return nil, nil
	}
	var capacity storagebudget.CapacityReader
	if cfg.StorageLVMVolumeGroup != "" {
		capacity = storagebudget.LVMCapacity{Reader: kube, Namespace: cfg.StorageLVMNamespace, VolumeGroup: cfg.StorageLVMVolumeGroup}
	} else {
		bytes, err := cfg.StorageNodeCapacityBytes()
		if err != nil {
			return nil, err
		}
		capacity = storagebudget.FixedCapacity(bytes)
	}
	return storagebudget.New(storagebudget.NewClusterSource(capacity, kube), cfg.StorageBudgetPercent), nil
}

// verifyTenantStorage refuses a production start whose tenant disks would not
// be held to their size, or whose budget cannot be read.
func verifyTenantStorage(ctx context.Context, cfg config.AppConfig, kube k8s.KubeClient, budget *storagebudget.Budget) error {
	if !cfg.TenantStorageRequireSized {
		return nil
	}
	if kube == nil {
		return fmt.Errorf("TENANT_STORAGE_REQUIRE_SIZED needs a Kubernetes client to check the tenant storage class")
	}
	classes := append([]string{cfg.TenantStorageClass}, cfg.TenantStorageClasses...)
	for _, class := range classes {
		if err := kube.RequireSizedStorageClass(ctx, class, cfg.TenantSizedProvisioners); err != nil {
			return fmt.Errorf("tenant storage: %w", err)
		}
	}
	if budget == nil {
		return fmt.Errorf("TENANT_STORAGE_REQUIRE_SIZED needs the storage budget's capacity (STORAGE_LVM_VOLUME_GROUP or STORAGE_NODE_CAPACITY)")
	}
	if _, err := budget.Report(ctx); err != nil {
		return fmt.Errorf("storage budget: the capacity could not be read: %w", err)
	}
	return nil
}

// holdClientToBudget makes every cluster, app disk and growth the client
// creates reserve its storage first.
func holdClientToBudget(kube k8s.KubeClient, budget *storagebudget.Budget) {
	if budget == nil {
		return
	}
	if client, ok := kube.(interface{ SetStorageReserver(k8s.StorageReserver) }); ok {
		client.SetStorageReserver(budget)
	}
}

// startStorageMetrics exports the budget for the 70% and 80% alerts.
func startStorageMetrics(ctx context.Context, budget *storagebudget.Budget) {
	if budget == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(storageMetricsRefresh)
		defer ticker.Stop()
		for {
			report, err := budget.Report(ctx)
			if err != nil {
				log.Printf("storage budget metrics: %v", err)
			} else {
				metrics.SetStorageBudget(report.CapacityBytes, report.BudgetBytes, report.AllocatedBytes)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
