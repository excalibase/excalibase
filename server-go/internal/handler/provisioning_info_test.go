package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	sqlitestore "github.com/excalibase/provisioning-poc/internal/storage/sqlite"
	"github.com/go-chi/chi/v5"
)

// setupInfoRouter wires the GetProjectInfo route with a real SQLite-backed
// OrgStore + InstanceStore so the test exercises the same code path used in
// production (handler -> service -> store -> orgStore.FindOrgByID).
func setupInfoRouter(t *testing.T) (chi.Router, *sqlitestore.Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := sqlitestore.New(dir + "/info.db")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	factory := provisioner.NewFactory()
	svc := service.NewProvisioningService(store, factory, nil)
	h := NewProvisioningHandler(svc, store)

	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/info", func(r chi.Router) {
		r.Get("/", h.GetProjectInfo)
	})
	return r, store
}

func TestGetProjectInfo_Success(t *testing.T) {
	r, store := setupInfoRouter(t)
	ctx := context.Background()

	if err := store.CreateUser(ctx, &domain.User{
		ID: "owner-1", Username: "owner1", Email: "owner1@test.com",
		PasswordHash: "hash", Role: "user", Active: true,
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if err := store.CreateOrg(ctx, &domain.Org{
		ID:      "e6ab0746-974d-4ed1-b900-d83e916f1d39",
		Name:    "Acme Corp",
		Slug:    "acme",
		Tier:    domain.Free,
		OwnerID: "owner-1",
	}); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	if err := store.Save(&domain.DatabaseInstance{
		ProjectID:   "proj-i1nd88wser",
		ProjectName: "blog",
		OrgID:       "e6ab0746-974d-4ed1-b900-d83e916f1d39",
		OwnerID:     "owner-1",
		Status:      "ACTIVE",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/projects/proj-i1nd88wser/info", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body=%s)", w.Code, w.Body.String())
	}

	var got domain.ProjectInfo
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	want := domain.ProjectInfo{
		ProjectID:   "proj-i1nd88wser",
		ProjectName: "blog",
		OrgID:       "e6ab0746-974d-4ed1-b900-d83e916f1d39",
		OrgSlug:     "acme",
		OrgName:     "Acme Corp",
	}
	if got != want {
		t.Errorf("body mismatch:\n got=%+v\nwant=%+v", got, want)
	}

	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type: got %q, want application/json", ct)
	}
}

func TestGetProjectInfo_NotFound(t *testing.T) {
	r, _ := setupInfoRouter(t)

	req := httptest.NewRequest("GET", "/api/projects/proj-doesnotexist/info", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404 (body=%s)", w.Code, w.Body.String())
	}
}

func TestGetProjectInfo_InvalidProjectID(t *testing.T) {
	r, _ := setupInfoRouter(t)

	// Path-traversal-ish — '..' not allowed by isValidID; chi will route on
	// the segment but the handler must reject it before any store lookup.
	req := httptest.NewRequest("GET", "/api/projects/bad..id/info", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 (body=%s)", w.Code, w.Body.String())
	}
}

// When the org record is missing (e.g. legacy instances or orphaned data),
// the handler still returns 200 with the orgID echoed for slug/name so the
// auth service can still mint a JWT with at least the projectID grounded.
func TestGetProjectInfo_OrgMissing_FallsBackToOrgID(t *testing.T) {
	r, store := setupInfoRouter(t)

	if err := store.Save(&domain.DatabaseInstance{
		ProjectID:   "proj-orphan",
		ProjectName: "orphaned",
		OrgID:       "unknown-org-id",
		Status:      "ACTIVE",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/projects/proj-orphan/info", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body=%s)", w.Code, w.Body.String())
	}

	var got domain.ProjectInfo
	json.NewDecoder(w.Body).Decode(&got)

	if got.OrgID != "unknown-org-id" || got.OrgSlug != "unknown-org-id" || got.OrgName != "unknown-org-id" {
		t.Errorf("expected fallback to orgID for slug/name; got %+v", got)
	}
	if got.ProjectName != "orphaned" {
		t.Errorf("projectName: got %q, want %q", got.ProjectName, "orphaned")
	}
}
