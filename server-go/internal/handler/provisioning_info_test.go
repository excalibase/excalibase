//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	pgtest "github.com/excalibase/provisioning-poc/internal/testutil/pgstore"
	"github.com/go-chi/chi/v5"
)

const (
	testOwner1 = "owner-1"
	testUUID   = "e6ab0746-974d-4ed1-b900-d83e916f1d39"
)

// setupInfoRouter wires the GetProjectInfo route with a real Postgres-backed
// OrgStore + InstanceStore so the test exercises the same code path used in
// production (handler -> service -> store -> orgStore.FindOrgByID).
func setupInfoRouter(t *testing.T) (chi.Router, *pgstore.Store) {
	t.Helper()
	store := pgtest.New(t)

	factory := provisioner.NewFactory()
	svc := service.NewProvisioningService(store, factory, nil)
	h := NewProvisioningHandler(svc, store)
	h.SetCorsStore(store)

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
		ID: testOwner1, Username: testutil.FixtureToken("owner1"), Email: "owner1@test.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true,
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if err := store.CreateOrg(ctx, &domain.Org{
		ID:      testUUID,
		Name:    "Acme Corp",
		Slug:    "acme",
		Tier:    domain.Free,
		OwnerID: testOwner1,
	}); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	if err := store.Create(&domain.DatabaseInstance{
		ProjectID:   "proj-i1nd88wser",
		ProjectName: "blog",
		OrgID:       testUUID,
		OwnerID:     testOwner1,
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
		ProjectID:          "proj-i1nd88wser",
		ProjectName:        "blog",
		OrgID:              testUUID,
		OrgSlug:            "acme",
		OrgName:            "Acme Corp",
		RealtimeAutoEnable: true,
		CorsAllowedOrigins: []string{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("body mismatch:\n got=%+v\nwant=%+v", got, want)
	}

	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type: got %q, want application/json", ct)
	}
}

// The data plane reads the CORS allowlist from /info, so the list a Developer
// wrote through /cors must round-trip through the same Postgres-backed store.
func TestGetProjectInfo_ExposesCorsAllowedOrigins(t *testing.T) {
	r, store := setupInfoRouter(t)
	ctx := context.Background()

	if err := store.CreateUser(ctx, &domain.User{
		ID: "owner-cors", Username: testutil.FixtureToken("ownercors"), Email: "ownercors@test.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true,
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := store.CreateOrg(ctx, &domain.Org{
		ID: "a1b2c3d4-0000-4000-8000-000000000001", Name: "Cors Org", Slug: "cors-org",
		Tier: domain.Free, OwnerID: "owner-cors",
	}); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "proj-cors-info", ProjectName: "web",
		OrgID: "a1b2c3d4-0000-4000-8000-000000000001", OwnerID: "owner-cors", Status: "ACTIVE",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	origins := []string{"http://localhost:5173", "https://app.example.com"}
	if err := store.SetCorsOrigins(ctx, "proj-cors-info", origins); err != nil {
		t.Fatalf("SetCorsOrigins: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/projects/proj-cors-info/info", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var got domain.ProjectInfo
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(got.CorsAllowedOrigins, origins) {
		t.Fatalf("corsAllowedOrigins = %v want %v", got.CorsAllowedOrigins, origins)
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

// When the org record can't be resolved, the handler must NOT forge a slug
// from the raw OrgID. Returning a UUID-as-slug feeds downstream JWT verifiers
// the wrong `iss` claim. 503 tells the caller to retry rather than caching a
// poisoned response.
func TestGetProjectInfo_OrgMissing_Returns503(t *testing.T) {
	r, store := setupInfoRouter(t)

	if err := store.Create(&domain.DatabaseInstance{
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

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want 503 (body=%s)", w.Code, w.Body.String())
	}
}
