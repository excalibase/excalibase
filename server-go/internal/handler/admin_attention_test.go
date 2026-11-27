package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
)

type fakeAttentionReader struct{ got adminconsole.AttentionQuery }

func (f *fakeAttentionReader) ListSuspended(_ context.Context, q adminconsole.AttentionQuery) (adminconsole.CursorPage[adminconsole.SuspendedBrief], error) {
	f.got = q
	return adminconsole.CursorPage[adminconsole.SuspendedBrief]{Items: []adminconsole.SuspendedBrief{{UserID: "u1"}}, Total: 1}, nil
}

func (f *fakeAttentionReader) ListFailedProjects(_ context.Context, q adminconsole.AttentionQuery) (adminconsole.CursorPage[adminconsole.ProjectBrief], error) {
	f.got = q
	return adminconsole.CursorPage[adminconsole.ProjectBrief]{Items: []adminconsole.ProjectBrief{}}, nil
}

var attentionPaths = []string{"/api/admin/attention/suspensions", "/api/admin/attention/failed-projects"}

func attentionRouter(role, scopes string, reader adminconsole.AttentionReader) http.Handler {
	h := NewAdminAttentionHandler(adminconsole.NewAttention(reader))
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.SetUser(req.Context(), &domain.User{ID: "boss", Role: role, Active: true})
			ctx = auth.SetToken(ctx, &domain.AccessToken{Scopes: scopes})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Route("/api/admin/attention", func(r chi.Router) { h.Routes(r) })
	return r
}

func attentionGet(router http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, strings.NewReader("")))
	return w
}

func TestAdminAttention_SameGateAsTheConsole(t *testing.T) {
	for _, role := range []string{"platform_operator", "platform_viewer", "user"} {
		router := attentionRouter(role, auth.ScopeSession, &fakeAttentionReader{})
		for _, path := range attentionPaths {
			if w := attentionGet(router, path); w.Code != http.StatusForbidden {
				t.Errorf("%s as %s = %d, want 403", path, role, w.Code)
			}
		}
	}
	token := attentionRouter("platform_admin", auth.ScopeRead, &fakeAttentionReader{})
	admin := attentionRouter("platform_admin", auth.ScopeSession, &fakeAttentionReader{})
	for _, path := range attentionPaths {
		if w := attentionGet(token, path); w.Code != http.StatusForbidden {
			t.Errorf("%s with an access token = %d, want 403", path, w.Code)
		}
		if w := attentionGet(admin, path); w.Code != http.StatusOK {
			t.Errorf("%s as platform_admin = %d: %s", path, w.Code, w.Body.String())
		}
	}
}

func TestAdminAttention_LimitIsCappedAndABadRequestIs400(t *testing.T) {
	reader := &fakeAttentionReader{}
	router := attentionRouter("platform_admin", auth.ScopeSession, reader)
	attentionGet(router, "/api/admin/attention/suspensions?limit=500")
	if reader.got.Limit != adminconsole.MaxAttentionPage {
		t.Fatalf("limit = %d, want the cap %d", reader.got.Limit, adminconsole.MaxAttentionPage)
	}
	for _, bad := range []string{"?cursor=garbage!", "?limit=abc", "?limit=-1"} {
		for _, path := range attentionPaths {
			if w := attentionGet(router, path+bad); w.Code != http.StatusBadRequest {
				t.Errorf("%s%s = %d, want 400", path, bad, w.Code)
			}
		}
	}
}
