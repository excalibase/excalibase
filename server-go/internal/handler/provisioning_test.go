package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

const (
	testProvisionPrefix = "/api/provision/"
	testWant200Fmt      = "status: got %d, want 200"
	testUser1           = "user-1"
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
	svc := service.NewProvisioningService(store, factory, nil)
	orgs := fakestore.NewOrgs()
	orgs.AddOrg("org", domain.Free)
	svc.SetOrgStore(orgs)
	// A non-nil orgStore is required: ListInstances now fails closed (503)
	// when the org store is missing rather than dumping every tenant's
	// instances. The fake returns no orgs, which is fine for these tests —
	// they exercise the no-user (unscoped) and platform-admin branches.
	h := NewProvisioningHandler(svc, &adminOrgStore{})

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

	req := httptest.NewRequest("GET", testProvisionPrefix, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf(testWant200Fmt, w.Code)
	}

	var result []*domain.DatabaseInstance
	json.NewDecoder(w.Body).Decode(&result)
	if len(result) != 0 {
		t.Errorf("expected empty list, got %d", len(result))
	}
}

func TestListInstancesWithData(t *testing.T) {
	r, store := setupTestRouter(t)

	store.Create(&domain.DatabaseInstance{
		ProjectID: "db1",
		OrgID:     "org1",
		Status:    "ACTIVE",
	})

	req := httptest.NewRequest("GET", testProvisionPrefix, nil)
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
	store.Create(&domain.DatabaseInstance{
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
		t.Errorf(testWant200Fmt, w.Code)
	}

	var inst domain.DatabaseInstance
	json.NewDecoder(w.Body).Decode(&inst)
	if inst.Status != "ACTIVE" {
		t.Errorf("status: got %s", inst.Status)
	}
}

func TestProvisionNoK8s(t *testing.T) {
	r, _ := setupTestRouter(t)

	body := `{"projectName":"test","orgId":"org","databaseType":"POSTGRESQL","postgresVersion":"17"}`
	req := httptest.NewRequest("POST", testProvisionPrefix, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// Authenticated platform_admin bypasses the create_project org check, so the
	// request reaches the provisioner and fails there (the path under test).
	req = req.WithContext(auth.SetUser(req.Context(), &domain.User{ID: "op", Role: "platform_admin", Active: true}))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Should fail because no provisioner registered for POSTGRESQL in empty factory
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 (no provisioner)", w.Code)
	}
}

func TestListInstancesReturnsAll(t *testing.T) {
	r, store := setupTestRouter(t)

	store.Create(&domain.DatabaseInstance{ProjectID: "a", OwnerID: testUser1, Status: "ACTIVE"})
	store.Create(&domain.DatabaseInstance{ProjectID: "b", OwnerID: testUser1, Status: "ACTIVE"})
	store.Create(&domain.DatabaseInstance{ProjectID: "c", OwnerID: "user-2", Status: "ACTIVE"})

	req := httptest.NewRequest("GET", testProvisionPrefix, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf(testWant200Fmt, w.Code)
	}

	var result []*domain.DatabaseInstance
	json.NewDecoder(w.Body).Decode(&result)
	// Without auth context and nil orgStore, returns all instances
	if len(result) != 3 {
		t.Fatalf("expected 3 instances, got %d", len(result))
	}
}

// TestListInstances_NilOrgStoreFailsClosed proves the all-tenant dump hole is
// closed: when the org store isn't wired the handler can't scope instances to
// the caller's orgs, so it must 503 rather than returning every tenant's
// instances. Previously a nil orgStore fell open and leaked the full list.
func TestListInstances_NilOrgStoreFailsClosed(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.NewFileSystemStore(dir)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	// Seed instances that would be leaked under the old fail-open behaviour.
	store.Create(&domain.DatabaseInstance{ProjectID: "a", OrgID: "org1", Status: "ACTIVE"})
	store.Create(&domain.DatabaseInstance{ProjectID: "b", OrgID: "org2", Status: "ACTIVE"})

	factory := provisioner.NewFactory()
	svc := service.NewProvisioningService(store, factory, nil)
	h := NewProvisioningHandler(svc, nil) // nil org store → must fail closed

	r := chi.NewRouter()
	r.Get("/api/provision/", h.ListInstances)

	req := httptest.NewRequest("GET", testProvisionPrefix, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil orgStore: got %d, want 503 (body=%s)", w.Code, w.Body.String())
	}
	// The body must NOT contain any instance data.
	if strings.Contains(w.Body.String(), `"projectId"`) {
		t.Errorf("503 response leaked instance data: %s", w.Body.String())
	}
}

func TestGetCredentialsForCDS(t *testing.T) {
	r, store := setupTestRouter(t)

	port := 5432
	pgUsername := testutil.FixtureToken("pguser")
	store.Create(&domain.DatabaseInstance{
		ProjectID:    "cds-project",
		OrgID:        "org1",
		OwnerID:      testUser1,
		Status:       "ACTIVE",
		Host:         "10.0.0.5",
		ReadOnlyHost: "10.0.0.6",
		Port:         &port,
		DatabaseName: "app_db",
		Username:     pgUsername,
		Password:     testutil.FixturePassword("cds-proj"),
		SSLMode:      "require",
	})

	req := httptest.NewRequest("GET", "/api/provision/cds-project/credentials", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf(testWant200Fmt, w.Code)
	}

	var creds struct {
		ProjectID     string `json:"projectId"`
		Host          string `json:"host"`
		ReadOnlyHost  string `json:"readOnlyHost"`
		Port          int    `json:"port"`
		DatabaseName  string `json:"databaseName"`
		Username      string `json:"username"`
		Password      string `json:"password"`
		SSLMode       string `json:"sslMode"`
		ConnectionURL string `json:"connectionUrl"`
	}
	json.NewDecoder(w.Body).Decode(&creds)

	if creds.Host != "10.0.0.5" {
		t.Errorf("host: got %s", creds.Host)
	}
	if creds.Port != 5432 {
		t.Errorf("port: got %d", creds.Port)
	}
	if creds.DatabaseName != "app_db" {
		t.Errorf("databaseName: got %s", creds.DatabaseName)
	}
	if creds.Username != pgUsername {
		t.Errorf("username: got %s", creds.Username)
	}
	if creds.Password != testutil.FixturePassword("cds-proj") {
		t.Errorf("password: got %s", creds.Password)
	}
	if creds.ConnectionURL == "" {
		t.Error("connectionUrl should be set")
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
		t.Errorf(testWant200Fmt, w.Code)
	}

	var est domain.CostEstimation
	json.NewDecoder(w.Body).Decode(&est)
	if est.MonthlyCostUSD != 49.99 {
		t.Errorf("cost: got %f, want 49.99", est.MonthlyCostUSD)
	}
}
