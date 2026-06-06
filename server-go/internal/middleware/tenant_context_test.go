package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

const testProjABC = "proj-abc"

// newReqWithChiParam simulates a chi-routed request by seeding the chi
// RouteContext with the given URL params. This mirrors how chi.URLParam
// behaves inside a real router without needing to stand one up.
func newReqWithChiParam(t *testing.T, params map[string]string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/provision/proj-123", nil)
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestTenantContext_ExtractsProjectID(t *testing.T) {
	var seenTenant string
	var seenOK bool

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenTenant, seenOK = TenantIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := newReqWithChiParam(t, map[string]string{"projectId": testProjABC})
	rr := httptest.NewRecorder()

	TenantContext(next).ServeHTTP(rr, req)

	if !seenOK {
		t.Fatal("tenant not propagated into request context")
	}
	if seenTenant != testProjABC {
		t.Errorf("tenant id: got %q, want %q", seenTenant, testProjABC)
	}
	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestTenantContext_AbsentProjectIDIsNoOp(t *testing.T) {
	var seenTenant string
	var seenOK bool
	nextCalled := false

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		seenTenant, seenOK = TenantIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	// No projectId in chi route params — simulates a non-tenant-scoped route.
	req := newReqWithChiParam(t, map[string]string{})
	rr := httptest.NewRecorder()

	TenantContext(next).ServeHTTP(rr, req)

	if !nextCalled {
		t.Fatal("next handler was not invoked — middleware must be a no-op when projectId is absent")
	}
	if seenOK {
		t.Errorf("TenantIDFromContext should return ok=false when projectId is absent, got ok=true tenant=%q", seenTenant)
	}
	if seenTenant != "" {
		t.Errorf("tenant id should be empty, got %q", seenTenant)
	}
}

func TestTenantIDFromContext_EmptyContext(t *testing.T) {
	tenant, ok := TenantIDFromContext(context.Background())
	if ok {
		t.Errorf("empty context: got ok=true tenant=%q, want ok=false", tenant)
	}
	if tenant != "" {
		t.Errorf("empty context: tenant should be empty, got %q", tenant)
	}
}

func TestTenantIDFromContext_NilContext(t *testing.T) {
	//nolint:staticcheck // intentionally passing nil context to exercise defensive check
	tenant, ok := TenantIDFromContext(nil)
	if ok || tenant != "" {
		t.Errorf("nil context: got (%q, %t), want (\"\", false)", tenant, ok)
	}
}

func TestTenantIDFromContext_EmptyStringValue(t *testing.T) {
	// Guard: if somebody pushes an empty string under the key, the helper
	// should still report "no tenant".
	ctx := context.WithValue(context.Background(), tenantIDKey, "")
	tenant, ok := TenantIDFromContext(ctx)
	if ok {
		t.Errorf("empty string value: got ok=true tenant=%q, want ok=false", tenant)
	}
}
