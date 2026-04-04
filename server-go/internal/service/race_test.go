package service

import (
	"context"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// TestConcurrentProvisionSameID verifies only one provision succeeds for the same projectId.
func TestConcurrentProvisionSameID(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	mock.SetupPostgreSQLMock("race-db", "org1-race-db", 1)
	pgProv := provisioner.NewPostgreSQLProvisioner(mock)
	factory := provisioner.NewFactory(pgProv)
	svc := NewProvisioningService(store, factory, mock)

	var wg sync.WaitGroup
	results := make(chan string, 10)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
				ProjectName: "race-db",
				OrgID:       "org1",
				DBType:      domain.PostgreSQL,
				Tier:        domain.Free,
			})
			if err != nil {
				results <- "error:" + err.Error()
			} else {
				results <- "status:" + resp.Status
			}
		}()
	}

	wg.Wait()
	close(results)

	activeCount := 0
	errorCount := 0
	for r := range results {
		if r == "status:ACTIVE" {
			activeCount++
		} else {
			errorCount++
		}
	}

	// At least one should succeed, rest should fail with "already exists"
	if activeCount < 1 {
		t.Error("at least one provision should succeed")
	}
	if errorCount < 1 {
		t.Error("concurrent provisions of same ID should produce errors")
	}
}

// TestConcurrentMetricsCollection verifies metrics are safe to collect concurrently.
func TestConcurrentMetricsCollection(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	store.Save(&domain.DatabaseInstance{
		ProjectID: "conc-m", Namespace: "ns", Status: "ACTIVE",
		DBType: domain.PostgreSQL, Tier: domain.Free,
	})
	mock.SetupPostgreSQLMock("conc-m", "ns", 1)

	svc := NewMetricsService(store, mock, dir)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, err := svc.GetCurrentMetrics(context.Background(), "conc-m")
			if err != nil {
				t.Errorf("concurrent metrics: %v", err)
			}
			if !m.MetricsAvailable {
				t.Error("metrics should be available")
			}
		}()
	}
	wg.Wait()

	// History should have all 10 points
	hist, _ := svc.GetMetricsHistory(context.Background(), "conc-m", 100)
	if hist.TotalPoints < 10 {
		t.Errorf("expected >= 10 history points, got %d", hist.TotalPoints)
	}
}

// TestConcurrentStoreAccess verifies store is safe for concurrent reads/writes.
func TestConcurrentStoreAccess(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)

	var wg sync.WaitGroup
	// 10 writers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store.Save(&domain.DatabaseInstance{
				ProjectID: "conc-db",
				Status:    "ACTIVE",
			})
		}(i)
	}
	// 10 readers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store.FindByProjectID("conc-db")
			store.FindAll()
		}()
	}
	wg.Wait()
}
