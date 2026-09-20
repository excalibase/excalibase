package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
)

// fakeProm implements promQuerier with canned values.
type fakeProm struct {
	cpu, mem float64
	err      error
}

func (f *fakeProm) InstantValue(_ context.Context, query string) (float64, error) {
	if f.err != nil {
		return 0, f.err
	}
	// Crude: CPU query mentions cpu, memory query mentions memory.
	if strings.Contains(query, "cpu") {
		return f.cpu, nil
	}
	return f.mem, nil
}

func adminWithStore(prom promQuerier) *AdminHandler {
	store := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj-1": {ProjectID: "proj-1", ProjectName: "One", OrgID: "org-1", Status: "ACTIVE", Namespace: "ns-1"},
		"proj-2": {ProjectID: "proj-2", ProjectName: "Two", OrgID: "org-2", Status: "ACTIVE", Namespace: "ns-2"},
	}}
	return NewAdminHandler(nil, store, nil, nil, nil, "", prom)
}

func TestAdmin_ListAllProjects_NoProm(t *testing.T) {
	h := adminWithStore(nil)
	req := httptest.NewRequest("GET", "/api/admin/projects", nil)
	w := httptest.NewRecorder()
	h.ListAllProjects(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	var out []map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &out)
	if len(out) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(out))
	}
	// Without prom, usage fields are absent.
	if _, ok := out[0]["cpuCores"]; ok {
		t.Error("cpuCores should be absent without prometheus")
	}
}

func TestAdmin_ListAllProjects_WithProm(t *testing.T) {
	h := adminWithStore(&fakeProm{cpu: 0.5, mem: 1024})
	req := httptest.NewRequest("GET", "/api/admin/projects", nil)
	w := httptest.NewRecorder()
	h.ListAllProjects(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	var out []map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &out)
	if len(out) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(out))
	}
	if out[0]["cpuCores"] == nil {
		t.Error("cpuCores should be present with prometheus")
	}
}

// errInstanceStore implements InstanceStore but FindAll fails.
type errInstanceStore struct{}

func (errInstanceStore) Create(*domain.DatabaseInstance) error { return nil }
func (errInstanceStore) Update(*domain.DatabaseInstance) error { return nil }
func (errInstanceStore) CreateWithinOrgLimit(*domain.DatabaseInstance, int) error {
	return nil
}
func (errInstanceStore) CountOrgProjects(string) (int, error) {
	return 0, errors.New("db down")
}
func (errInstanceStore) FindByProjectID(string) (*domain.DatabaseInstance, error) {
	return nil, nil
}
func (errInstanceStore) FindByOwner(string) ([]*domain.DatabaseInstance, error) { return nil, nil }
func (errInstanceStore) FindAll() ([]*domain.DatabaseInstance, error) {
	return nil, errors.New("db down")
}
func (errInstanceStore) Delete(string) error { return nil }
func (errInstanceStore) BeginDeletion(string, *bool) (bool, error) {
	return false, errors.New("db down")
}
func (errInstanceStore) UpdateIfStatus(*domain.DatabaseInstance, string) error {
	return nil
}

func (errInstanceStore) RecordPauseAttempt(string, time.Time) (int, error) {
	return 0, nil
}

func (errInstanceStore) RecordRestoreInterrupted(string, string, string) error {
	return nil
}

func (errInstanceStore) RecordDeletionFailure(string, domain.ProvisioningStage, string, string) error {
	return errors.New("db down")
}

func TestAdmin_ListAllProjects_StoreError(t *testing.T) {
	h := NewAdminHandler(nil, errInstanceStore{}, nil, nil, nil, "", nil)
	req := httptest.NewRequest("GET", "/api/admin/projects", nil)
	w := httptest.NewRecorder()
	h.ListAllProjects(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("store error should 500, got %d", w.Code)
	}
}

func TestAdmin_ForceDropProject_NotFound(t *testing.T) {
	h := NewAdminHandler(nil, &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{}}, nil, nil, nil, "", nil)
	r := chi.NewRouter()
	r.Delete("/api/admin/projects/{projectId}", h.ForceDropProject)
	req := httptest.NewRequest("DELETE", "/api/admin/projects/ghost", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown project should 404, got %d", w.Code)
	}
}

func TestAdmin_RevokeOrg_RequiresCascade(t *testing.T) {
	h := NewAdminHandler(nil, &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{}}, nil, nil, nil, "", nil)
	r := chi.NewRouter()
	r.Delete("/api/admin/orgs/{orgId}", h.RevokeOrg)
	// No ?cascade=true → 400.
	req := httptest.NewRequest("DELETE", "/api/admin/orgs/org-1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing cascade should 400, got %d", w.Code)
	}
}

func TestAdmin_RevokeOrg_NoOrgStore(t *testing.T) {
	h := NewAdminHandler(nil, &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{}}, nil, nil, nil, "", nil)
	r := chi.NewRouter()
	r.Delete("/api/admin/orgs/{orgId}", h.RevokeOrg)
	req := httptest.NewRequest("DELETE", "/api/admin/orgs/org-1?cascade=true", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("nil org store should 503, got %d", w.Code)
	}
}

func TestAdmin_QueryLogs_NoLokiURL(t *testing.T) {
	h := NewAdminHandler(nil, &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{}}, nil, nil, nil, "", nil)
	req := httptest.NewRequest("GET", "/api/admin/logs", nil)
	w := httptest.NewRecorder()
	h.QueryLogs(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("missing LOKI_URL should 503, got %d", w.Code)
	}
}
