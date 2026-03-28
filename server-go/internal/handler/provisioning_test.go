package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

func setupTestRouter(t *testing.T) (chi.Router, *storage.FileSystemStore) {
	t.Helper()
	dir := t.TempDir()
	store, err := storage.NewFileSystemStore(dir)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}

	// Empty factory (no real K8s provisioners for unit tests)
	factory := provisioner.NewFactory()
	svc := service.NewProvisioningService(store, factory)
	h := NewProvisioningHandler(svc)

	r := chi.NewRouter()
	r.Route("/api/provision", func(r chi.Router) {
		r.Get("/", h.ListInstances)
		r.Post("/", h.Provision)
		r.Post("/estimate", h.EstimateCost)
		r.Route("/{projectId}", func(r chi.Router) {
			r.Get("/", h.GetStatus)
			r.Delete("/", h.Delete)
			r.Get("/credentials", h.GetCredentials)
		})
	})

	return r, store
}

func TestListInstancesEmpty(t *testing.T) {
	r, _ := setupTestRouter(t)

	req := httptest.NewRequest("GET", "/api/provision/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}

	var result []*domain.DatabaseInstance
	json.NewDecoder(w.Body).Decode(&result)
	if len(result) != 0 {
		t.Errorf("expected empty list, got %d", len(result))
	}
}

func TestListInstancesWithData(t *testing.T) {
	r, store := setupTestRouter(t)

	store.Save(&domain.DatabaseInstance{
		ProjectID: "db1",
		OrgID:     "org1",
		Status:    "ACTIVE",
	})

	req := httptest.NewRequest("GET", "/api/provision/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var result []*domain.DatabaseInstance
	json.NewDecoder(w.Body).Decode(&result)
	if len(result) != 1 {
		t.Errorf("expected 1 instance, got %d", len(result))
	}
	if result[0].ProjectID != "db1" {
		t.Errorf("projectId: got %s", result[0].ProjectID)
	}
}

func TestGetStatusNotFound(t *testing.T) {
	r, _ := setupTestRouter(t)

	req := httptest.NewRequest("GET", "/api/provision/nonexistent", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", w.Code)
	}
}

func TestGetStatus(t *testing.T) {
	r, store := setupTestRouter(t)

	port := 5432
	store.Save(&domain.DatabaseInstance{
		ProjectID:    "test-db",
		OrgID:        "org1",
		Status:       "ACTIVE",
		CurrentStage: domain.StageCompleted,
		Host:         "test.local",
		Port:         &port,
	})

	req := httptest.NewRequest("GET", "/api/provision/test-db", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}

	var inst domain.DatabaseInstance
	json.NewDecoder(w.Body).Decode(&inst)
	if inst.Status != "ACTIVE" {
		t.Errorf("status: got %s", inst.Status)
	}
}

func TestProvisionNoK8s(t *testing.T) {
	r, _ := setupTestRouter(t)

	body := `{"projectName":"test","orgId":"org","databaseType":"POSTGRESQL","tier":"FREE"}`
	req := httptest.NewRequest("POST", "/api/provision/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Should fail because no provisioner registered for POSTGRESQL in empty factory
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 (no provisioner)", w.Code)
	}
}

func TestEstimateCost(t *testing.T) {
	r, _ := setupTestRouter(t)

	body := `{"tier":"STANDARD"}`
	req := httptest.NewRequest("POST", "/api/provision/estimate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}

	var est domain.CostEstimation
	json.NewDecoder(w.Body).Decode(&est)
	if est.MonthlyCostUSD != 49.99 {
		t.Errorf("cost: got %f, want 49.99", est.MonthlyCostUSD)
	}
}
