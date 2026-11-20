package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
)

// failingConsole fails every read with err.
type failingConsole struct{ err error }

func (f failingConsole) Users(context.Context, adminconsole.UserQuery) (adminconsole.Page[adminconsole.UserRow], error) {
	return adminconsole.Page[adminconsole.UserRow]{}, f.err
}
func (f failingConsole) Orgs(context.Context, adminconsole.OrgQuery) (adminconsole.Page[adminconsole.OrgRow], error) {
	return adminconsole.Page[adminconsole.OrgRow]{}, f.err
}
func (f failingConsole) Overview(context.Context) (*adminconsole.Overview, error) { return nil, f.err }
func (f failingConsole) Project(context.Context, adminconsole.Actor, string, string) (*adminconsole.ProjectDetail, error) {
	return nil, f.err
}

func failingRouter(err error) http.Handler {
	return consoleRouterFor("platform_admin", failingConsole{err}, &fakeSuspender{err: err})
}

// A failed read or action answers with a status that says what went wrong and
// never leaks the store's own message.
func TestAdminConsole_FailuresMapToStatusesWithoutInternals(t *testing.T) {
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/api/admin/console/overview", ""},
		{http.MethodGet, "/api/admin/console/users", ""},
		{http.MethodGet, "/api/admin/console/orgs", ""},
		{http.MethodGet, "/api/admin/console/projects/p1?reason=checking+an+abuse+report", ""},
		{http.MethodPost, "/api/admin/console/users/u1/suspend", `{"reason":"proxy abuse confirmed"}`},
		{http.MethodPost, "/api/admin/console/users/u1/unsuspend", `{"reason":"appealed and verified"}`},
	}
	cases := map[string]struct {
		err  error
		want int
	}{
		"missing":        {adminconsole.ErrUserNotFound, http.StatusNotFound},
		"no reason":      {adminconsole.ErrReasonRequired, http.StatusBadRequest},
		"reason too big": {adminconsole.ErrReasonTooLong, http.StatusBadRequest},
		"protected":      {adminconsole.ErrProtectedAccount, http.StatusConflict},
		"store down":     {errors.New("pq: connection refused at 10.0.0.5"), http.StatusInternalServerError},
	}
	for name, tc := range cases {
		router := failingRouter(tc.err)
		for _, route := range routes {
			w := consoleCall(router, route.method, route.path, route.body)
			if w.Code != tc.want {
				t.Errorf("%s: %s %s = %d, want %d", name, route.method, route.path, w.Code, tc.want)
			}
			if tc.want == http.StatusInternalServerError && strings.Contains(w.Body.String(), "10.0.0.5") {
				t.Errorf("%s: the response leaks the store's message: %s", name, w.Body.String())
			}
		}
	}
}

func TestAdminConsole_AMalformedActionBodyIsRefused(t *testing.T) {
	router := consoleRouter("platform_admin", &fakeConsole{}, &fakeSuspender{})
	for _, path := range []string{"/api/admin/console/users/u1/suspend", "/api/admin/console/users/u1/unsuspend"} {
		if w := consoleCall(router, http.MethodPost, path, `not json`); w.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", path, w.Code)
		}
	}
}
