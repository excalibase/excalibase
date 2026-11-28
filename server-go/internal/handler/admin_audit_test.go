package handler

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/lib/pq"
)

type fakeAuditRead struct{ got adminconsole.AuditQuery }

func (f *fakeAuditRead) ListAudit(_ context.Context, q adminconsole.AuditQuery) (adminconsole.AuditPage, error) {
	f.got = q
	return adminconsole.AuditPage{Items: []adminconsole.AuditRow{{ID: "5", Action: "user.suspend"}}}, nil
}

type fakeDetailRead struct{ asked string }

func (f *fakeDetailRead) ReadUserDetail(_ context.Context, id string) (*adminconsole.UserDetail, error) {
	f.asked = id
	if id == "missing" {
		return nil, adminconsole.ErrUserNotFound
	}
	return &adminconsole.UserDetail{Profile: adminconsole.UserProfile{ID: id, Email: "a@x.test"}}, nil
}

func auditRouter(role, scopes string, audit *fakeAuditRead, detail *fakeDetailRead) http.Handler {
	h := NewAdminAuditHandler(adminconsole.NewAuditLog(audit), adminconsole.NewUserDetails(detail, nil))
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.SetUser(req.Context(), &domain.User{ID: "boss", Role: role, Active: true})
			ctx = auth.SetToken(ctx, &domain.AccessToken{Scopes: scopes})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Route("/api/admin/audit", func(r chi.Router) { h.AuditRoutes(r) })
	r.Route("/api/admin/accounts", func(r chi.Router) { h.AccountRoutes(r) })
	return r
}

func auditGet(router http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, strings.NewReader("")))
	return w
}

var auditPaths = []string{"/api/admin/audit/", "/api/admin/accounts/u-1"}

func TestAdminAudit_StaffSessionReads(t *testing.T) {
	router := auditRouter("platform_admin", auth.ScopeSession, &fakeAuditRead{}, &fakeDetailRead{})
	for _, path := range auditPaths {
		if w := auditGet(router, path); w.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", path, w.Code)
		}
	}
}

// A caller who is not platform_admin on a session learns nothing: the answer
// is the one an unmounted route gives, byte for byte.
func TestAdminAudit_EveryoneElseGetsTheUnmountedAnswer(t *testing.T) {
	callers := []struct{ name, role, scopes string }{
		{"customer", "user", auth.ScopeSession},
		{"operator", "platform_operator", auth.ScopeSession},
		{"viewer", "platform_viewer", auth.ScopeSession},
		{"admin token", "platform_admin", "read,write"},
		{"admin read-only token", "platform_admin", auth.ScopeRead},
	}
	for _, caller := range callers {
		router := auditRouter(caller.role, caller.scopes, &fakeAuditRead{}, &fakeDetailRead{})
		want := auditGet(router, "/api/admin/nothing-here")
		for _, path := range auditPaths {
			got := auditGet(router, path)
			if got.Code != want.Code || got.Body.String() != want.Body.String() || got.Header().Get("Content-Type") != want.Header().Get("Content-Type") {
				t.Errorf("%s on %s: %d %q %q, want the unmounted %d %q %q", caller.name, path, got.Code, got.Body.String(), got.Header().Get("Content-Type"), want.Code, want.Body.String(), want.Header().Get("Content-Type"))
			}
		}
	}
}

func TestAdminAudit_PassesFiltersAndCursorThrough(t *testing.T) {
	audit := &fakeAuditRead{}
	router := auditRouter("platform_admin", auth.ScopeSession, audit, &fakeDetailRead{})
	cursor := adminconsole.EncodeCursor(adminconsole.Cursor{ID: "40"})
	query := url.Values{"actor": {"boss@x.test"}, "action": {"user."}, "target": {"u-1"}, "cursor": {cursor}, "limit": {"10"}}
	if w := auditGet(router, "/api/admin/audit/?"+query.Encode()); w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	got := audit.got
	if got.Actor != "boss@x.test" || got.Action != "user." || got.Target != "u-1" || got.BeforeID != 40 || got.Limit != 10 {
		t.Fatalf("query = %+v", got)
	}
}

func TestAdminAudit_BadInputIs400(t *testing.T) {
	router := auditRouter("platform_admin", auth.ScopeSession, &fakeAuditRead{}, &fakeDetailRead{})
	for _, path := range []string{"/api/admin/audit/?cursor=!!", "/api/admin/audit/?limit=abc", "/api/admin/audit/?actor=" + strings.Repeat("a", 201)} {
		if w := auditGet(router, path); w.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", path, w.Code)
		}
	}
}

func TestAdminAccounts_UnknownUserIs404WithoutDetail(t *testing.T) {
	router := auditRouter("platform_admin", auth.ScopeSession, &fakeAuditRead{}, &fakeDetailRead{})
	w := auditGet(router, "/api/admin/accounts/missing")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestAdminAccounts_DatabaseFailureIsABare500(t *testing.T) {
	h := NewAdminAuditHandler(adminconsole.NewAuditLog(brokenAuditRead{}), adminconsole.NewUserDetails(brokenDetailRead{}, nil))
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.SetUser(req.Context(), &domain.User{ID: "boss", Role: "platform_admin", Active: true})
			next.ServeHTTP(w, req.WithContext(auth.SetToken(ctx, &domain.AccessToken{Scopes: auth.ScopeSession})))
		})
	})
	r.Route("/api/admin/audit", func(r chi.Router) { h.AuditRoutes(r) })
	r.Route("/api/admin/accounts", func(r chi.Router) { h.AccountRoutes(r) })
	for _, path := range auditPaths {
		w := auditGet(r, path)
		if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "pq:") || strings.Contains(w.Body.String(), "secret-detail") {
			t.Errorf("%s = %d %q, want a bare 500", path, w.Code, w.Body.String())
		}
	}
}

type brokenAuditRead struct{}

func (brokenAuditRead) ListAudit(context.Context, adminconsole.AuditQuery) (adminconsole.AuditPage, error) {
	return adminconsole.AuditPage{}, context.DeadlineExceeded
}

type brokenDetailRead struct{}

func (brokenDetailRead) ReadUserDetail(context.Context, string) (*adminconsole.UserDetail, error) {
	return nil, &detailErr{}
}

type detailErr struct{}

func (*detailErr) Error() string { return "pq: secret-detail" }

func TestAdminAudit_NoSignedInUserGetsTheUnmountedAnswer(t *testing.T) {
	h := NewAdminAuditHandler(adminconsole.NewAuditLog(&fakeAuditRead{}), adminconsole.NewUserDetails(&fakeDetailRead{}, nil))
	r := chi.NewRouter()
	r.Route("/api/admin/audit", func(r chi.Router) { h.AuditRoutes(r) })
	if w := auditGet(r, "/api/admin/audit/"); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 and no panic", w.Code)
	}
}

type slowAuditRead struct{}

func (slowAuditRead) ListAudit(context.Context, adminconsole.AuditQuery) (adminconsole.AuditPage, error) {
	return adminconsole.AuditPage{}, fmt.Errorf("audit page: %w", &pq.Error{Code: "57014", Message: "canceling statement due to statement timeout"})
}

func TestAdminAudit_AStatementTimeoutIs503AskingToNarrowTheFilterAndIsLoggedAsAWarning(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)
	h := NewAdminAuditHandler(adminconsole.NewAuditLog(slowAuditRead{}), adminconsole.NewUserDetails(&fakeDetailRead{}, nil))
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.SetUser(req.Context(), &domain.User{ID: "boss", Role: "platform_admin", Active: true})
			next.ServeHTTP(w, req.WithContext(auth.SetToken(ctx, &domain.AccessToken{Scopes: auth.ScopeSession})))
		})
	})
	r.Route("/api/admin/audit", func(r chi.Router) { h.AuditRoutes(r) })
	w := auditGet(r, "/api/admin/audit/?action=a")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "narrow the filter") || strings.Contains(w.Body.String(), "pq") || strings.Contains(w.Body.String(), "statement") {
		t.Fatalf("body = %q, want a plain ask to narrow the filter", w.Body.String())
	}
	if !strings.Contains(logged.String(), "WARN") || strings.Contains(logged.String(), "ERROR") {
		t.Fatalf("log = %q, want a WARN line and no ERROR", logged.String())
	}
}
