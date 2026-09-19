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

const testConcM = "conc-m"

// TestConcurrentProvisionSameDisplayName verifies that concurrent provisions with
// the same display name all succeed and receive distinct generated refs.
// After the project-ref refactor, same display name is no longer a conflict.
func TestConcurrentProvisionSameDisplayName(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	mock.WildcardPodReady = true
	mock.WildcardSecret = map[string][]byte{
		"username": []byte("app"),
		"password": []byte("testpassword123"),
		"dbname":   []byte("app"),
	}
	pgProv := provisioner.NewPostgreSQLProvisioner(mock, "")
	factory := provisioner.NewFactory(pgProv)
	svc := NewProvisioningService(store, factory, mock)
	// STANDARD tier so we can create >1 project per org
	var wg sync.WaitGroup
	results := make(chan string, 5)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
				ProjectName: "race-db",
				OrgID:       "org1",
				DBType:      domain.PostgreSQL,
				Tier:        domain.Enterprise,
			})
			if err != nil {
				results <- "error:" + err.Error()
			} else {
				results <- resp.ProjectID
			}
		}()
	}

	wg.Wait()
	close(results)

	refs := make(map[string]bool)
	for r := range results {
		if len(r) > 6 && r[:6] == "error:" {
			t.Errorf("concurrent provision failed: %s", r)
			continue
		}
		if refs[r] {
			t.Errorf("duplicate ref generated: %s", r)
		}
		refs[r] = true
	}
	if len(refs) != 5 {
		t.Errorf("expected 5 unique refs, got %d", len(refs))
	}
}

// TestConcurrentMetricsCollection verifies metrics are safe to collect concurrently.
func TestConcurrentMetricsCollection(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	store.Create(&domain.DatabaseInstance{
		ProjectID: testConcM, Namespace: "ns", Status: "ACTIVE",
		DBType: domain.PostgreSQL, Tier: domain.Free,
	})
	mock.SetupPostgreSQLMock(testConcM, "ns", 1)

	svc := NewMetricsService(store, mock, dir)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, err := svc.GetCurrentMetrics(context.Background(), testConcM)
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
	hist, _ := svc.GetMetricsHistory(context.Background(), testConcM, 100)
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
			store.Create(&domain.DatabaseInstance{
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
