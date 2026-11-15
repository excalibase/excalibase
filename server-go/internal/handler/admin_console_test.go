package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
)

type fakeConsole struct {
	lastUsers adminconsole.UserQuery
	lastOrgs  adminconsole.OrgQuery
	project   *adminconsole.ProjectDetail
	projectEr error
}

func (f *fakeConsole) Users(_ context.Context, q adminconsole.UserQuery) (adminconsole.Page[adminconsole.UserRow], error) {
	f.lastUsers = q
	return adminconsole.Page[adminconsole.UserRow]{Items: []adminconsole.UserRow{{ID: "u1", Email: "a@x.test", Status: "active"}}, Total: 1}, nil
}
func (f *fakeConsole) Orgs(_ context.Context, q adminconsole.OrgQuery) (adminconsole.Page[adminconsole.OrgRow], error) {
	f.lastOrgs = q
	return adminconsole.Page[adminconsole.OrgRow]{Items: []adminconsole.OrgRow{}}, nil
}
func (f *fakeConsole) Overview(context.Context) (*adminconsole.Overview, error) {
	return &adminconsole.Overview{Signups24h: 7}, nil
}
func (f *fakeConsole) Project(context.Context, string) (*adminconsole.ProjectDetail, error) {
	return f.project, f.projectEr
}

type fakeSuspender struct {
	suspended   []string
	reason      string
	actor       adminconsole.Actor
	unsuspended []string
	err         error
}

func (f *fakeSuspender) Suspend(_ context.Context, actor adminconsole.Actor, userID, reason string) (*adminconsole.Result, error) {
	f.suspended, f.reason, f.actor = append(f.suspended, userID), reason, actor
	return &adminconsole.Result{UserID: userID, Status: adminconsole.StatusSuspended}, f.err
}
func (f *fakeSuspender) Unsuspend(_ context.Context, _ adminconsole.Actor, userID string) (*adminconsole.Result, error) {
	f.unsuspended = append(f.unsuspended, userID)
	return &adminconsole.Result{UserID: userID, Status: adminconsole.StatusActive}, f.err
}

func consoleRouter(role string, console *fakeConsole, suspender *fakeSuspender) http.Handler {
	h := NewAdminConsoleHandler(console, suspender)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.SetUser(req.Context(), &domain.User{ID: "boss", Role: role, Active: true})
			ctx = auth.SetToken(ctx, &domain.AccessToken{Scopes: auth.ScopeSession})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Route("/api/admin", func(r chi.Router) { h.Routes(r) })
	return r
}

func consoleCall(router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestAdminConsole_OnlyAPlatformAdminMayCall(t *testing.T) {
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/api/admin/console/overview", ""},
		{http.MethodGet, "/api/admin/console/users", ""},
		{http.MethodGet, "/api/admin/console/orgs", ""},
		{http.MethodGet, "/api/admin/console/projects/p1", ""},
		{http.MethodPost, "/api/admin/console/users/u1/suspend", `{"reason":"x"}`},
		{http.MethodPost, "/api/admin/console/users/u1/unsuspend", ""},
	}
	for _, role := range []string{"platform_operator", "platform_viewer", "user"} {
		router := consoleRouter(role, &fakeConsole{project: &adminconsole.ProjectDetail{}}, &fakeSuspender{})
		for _, route := range routes {
			if w := consoleCall(router, route.method, route.path, route.body); w.Code != http.StatusForbidden {
				t.Errorf("%s %s as %s = %d, want 403", route.method, route.path, role, w.Code)
			}
		}
	}
	router := consoleRouter("platform_admin", &fakeConsole{project: &adminconsole.ProjectDetail{}}, &fakeSuspender{})
	for _, route := range routes {
		if w := consoleCall(router, route.method, route.path, route.body); w.Code != http.StatusOK {
			t.Errorf("%s %s as platform_admin = %d: %s", route.method, route.path, w.Code, w.Body.String())
		}
	}
}

func TestAdminConsole_UsersPassTheFilterAndHideSuspendedByDefault(t *testing.T) {
	console := &fakeConsole{}
	router := consoleRouter("platform_admin", console, &fakeSuspender{})

	consoleCall(router, http.MethodGet, "/api/admin/console/users?q=%40mailinator.test&limit=10&offset=20", "")
	if console.lastUsers.Search != "@mailinator.test" || console.lastUsers.Limit != 10 || console.lastUsers.Offset != 20 || console.lastUsers.IncludeSuspended {
		t.Fatalf("query = %+v", console.lastUsers)
	}
	consoleCall(router, http.MethodGet, "/api/admin/console/users?includeSuspended=true", "")
	if !console.lastUsers.IncludeSuspended {
		t.Fatal("includeSuspended=true must reach the service")
	}
}

func TestAdminConsole_SuspendPassesTheReasonAndTheActor(t *testing.T) {
	suspender := &fakeSuspender{}
	router := consoleRouter("platform_admin", &fakeConsole{}, suspender)

	w := consoleCall(router, http.MethodPost, "/api/admin/console/users/u7/suspend", `{"reason":"proxy abuse"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", w.Code, w.Body.String())
	}
	if len(suspender.suspended) != 1 || suspender.suspended[0] != "u7" || suspender.reason != "proxy abuse" || suspender.actor.ID != "boss" {
		t.Fatalf("suspender = %+v", suspender)
	}
	var body adminconsole.Result
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil || body.Status != adminconsole.StatusSuspended {
		t.Fatalf("body = %+v, %v", body, err)
	}
}

func TestAdminConsole_RefusalsMapToStatusCodes(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{adminconsole.ErrUserNotFound, http.StatusNotFound},
		{adminconsole.ErrReasonRequired, http.StatusBadRequest},
		{adminconsole.ErrProtectedAccount, http.StatusConflict},
		{errors.New("pq: connection to 10.0.0.5 refused"), http.StatusInternalServerError},
	}
	for _, c := range cases {
		router := consoleRouter("platform_admin", &fakeConsole{}, &fakeSuspender{err: c.err})
		w := consoleCall(router, http.MethodPost, "/api/admin/console/users/u1/suspend", `{"reason":"x"}`)
		if w.Code != c.want {
			t.Errorf("%v: code = %d, want %d", c.err, w.Code, c.want)
		}
		if c.want == http.StatusInternalServerError && strings.Contains(w.Body.String(), "10.0.0.5") {
			t.Errorf("an internal error leaked its detail: %s", w.Body.String())
		}
	}
}

func TestAdminConsole_UnknownProjectIs404(t *testing.T) {
	router := consoleRouter("platform_admin", &fakeConsole{projectEr: adminconsole.ErrProjectNotFound}, &fakeSuspender{})
	if w := consoleCall(router, http.MethodGet, "/api/admin/console/projects/nope", ""); w.Code != http.StatusNotFound {
		t.Fatalf("code = %d", w.Code)
	}
}

func TestAdminConsole_MalformedSuspendBodyIs400(t *testing.T) {
	router := consoleRouter("platform_admin", &fakeConsole{}, &fakeSuspender{})
	if w := consoleCall(router, http.MethodPost, "/api/admin/console/users/u1/suspend", `{`); w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d", w.Code)
	}
}

func TestAdminConsole_ANarrowedTokenIsRefusedEvenForAPlatformAdmin(t *testing.T) {
	h := NewAdminConsoleHandler(&fakeConsole{}, &fakeSuspender{})
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.SetUser(req.Context(), &domain.User{ID: "boss", Role: "platform_admin", Active: true})
			ctx = auth.SetToken(ctx, &domain.AccessToken{Scopes: "read", ProjectID: "p1"})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Route("/api/admin", func(r chi.Router) { h.Routes(r) })
	if w := consoleCall(r, http.MethodGet, "/api/admin/console/users", ""); w.Code != http.StatusForbidden {
		t.Fatalf("code = %d", w.Code)
	}
}
