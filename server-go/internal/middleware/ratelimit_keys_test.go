package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
)

func reqWithUser(id string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	if id != "" {
		ctx := auth.SetUser(r.Context(), &domain.User{ID: id})
		r = r.WithContext(ctx)
	}
	return r
}

// reqWithTenant routes a request through chi so {projectId} resolves, then runs
// TenantContext to attach the tenant id to the context.
func reqWithTenant(projectID string) *http.Request {
	r := httptest.NewRequest("GET", "/api/projects/"+projectID, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("projectId", projectID)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

	var captured *http.Request
	TenantContext(http.HandlerFunc(func(_ http.ResponseWriter, rr *http.Request) {
		captured = rr
	})).ServeHTTP(httptest.NewRecorder(), r)
	return captured
}

func TestPerUser(t *testing.T) {
	if got := PerUser(reqWithUser("user-42")); got != "u:user-42" {
		t.Errorf("PerUser: got %q, want u:user-42", got)
	}
	if got := PerUser(reqWithUser("")); got != "" {
		t.Errorf("PerUser without user: got %q, want empty", got)
	}
}

func TestPerProjectAndUser(t *testing.T) {
	// Both project + user present.
	r := reqWithTenant("proj-1")
	r = r.WithContext(auth.SetUser(r.Context(), &domain.User{ID: "u9"}))
	if got := PerProjectAndUser(r); got != "p:proj-1/u:u9" {
		t.Errorf("PerProjectAndUser: got %q", got)
	}
}

func TestPerProjectAndUser_OnlyUser(t *testing.T) {
	r := reqWithUser("solo")
	if got := PerProjectAndUser(r); got != "p:/u:solo" {
		t.Errorf("PerProjectAndUser user-only: got %q", got)
	}
}

func TestPerProjectAndUser_Neither(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if got := PerProjectAndUser(r); got != "" {
		t.Errorf("PerProjectAndUser with neither: got %q, want empty", got)
	}
}

func TestProjectIDFromURL(t *testing.T) {
	if got := projectIDFromURL(reqWithTenant("proj-x")); got != "proj-x" {
		t.Errorf("projectIDFromURL: got %q, want proj-x", got)
	}
	// No tenant attached → empty.
	if got := projectIDFromURL(httptest.NewRequest("GET", "/", nil)); got != "" {
		t.Errorf("projectIDFromURL without tenant: got %q", got)
	}
}
