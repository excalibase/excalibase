package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
)

// strictConsole refuses what the real console refuses, so the handler's answer
// to a bad sort can be seen without the database behind it.
type strictConsole struct{ fakeConsole }

func (s *strictConsole) Users(ctx context.Context, q adminconsole.UserQuery) (adminconsole.Page[adminconsole.UserRow], error) {
	return adminconsole.NewConsole(&refusingReader{}, nil, nil).Users(ctx, q)
}

func (s *strictConsole) Orgs(ctx context.Context, q adminconsole.OrgQuery) (adminconsole.Page[adminconsole.OrgRow], error) {
	return adminconsole.NewConsole(&refusingReader{}, nil, nil).Orgs(ctx, q)
}

type refusingReader struct{ adminconsole.Reader }

func (*refusingReader) ListUsers(context.Context, adminconsole.UserQuery) (adminconsole.Page[adminconsole.UserRow], error) {
	return adminconsole.Page[adminconsole.UserRow]{Items: []adminconsole.UserRow{}}, nil
}

func (*refusingReader) ListOrgs(context.Context, adminconsole.OrgQuery) (adminconsole.Page[adminconsole.OrgRow], error) {
	return adminconsole.Page[adminconsole.OrgRow]{Items: []adminconsole.OrgRow{}}, nil
}

func TestAdminConsole_UsersPassSortOrderAndFilters(t *testing.T) {
	console := &fakeConsole{}
	router := consoleRouter("platform_admin", console, &fakeSuspender{})
	consoleCall(router, http.MethodGet, "/api/admin/console/users?sort=email&order=desc&status=suspended&role=user", "")
	q := console.lastUsers
	if q.Sort != "email" || !q.Desc || q.Status != "suspended" || q.Role != "user" {
		t.Fatalf("query = %+v", q)
	}
	consoleCall(router, http.MethodGet, "/api/admin/console/users?sort=email", "")
	if console.lastUsers.Desc {
		t.Fatal("a named sort without an order is ascending")
	}
}

func TestAdminConsole_OrgsPassSortOrderAndFilters(t *testing.T) {
	console := &fakeConsole{}
	router := consoleRouter("platform_admin", console, &fakeSuspender{})
	consoleCall(router, http.MethodGet, "/api/admin/console/orgs?q=acme&sort=projects&order=desc&tier=standard&ownerStatus=suspended", "")
	q := console.lastOrgs
	if q.Search != "acme" || q.Sort != "projects" || !q.Desc || q.Tier != "standard" || q.OwnerStatus != "suspended" {
		t.Fatalf("query = %+v", q)
	}
}

func TestAdminConsole_AnUnknownSortFilterOrOrderIsA400(t *testing.T) {
	router := consoleRouterFor("platform_admin", &strictConsole{}, &fakeSuspender{})
	for _, path := range []string{
		"/api/admin/console/users?sort=password_hash",
		"/api/admin/console/users?sort=email%3BDROP%20TABLE%20users",
		"/api/admin/console/users?sort=email&order=sideways",
		"/api/admin/console/users?status=gone",
		"/api/admin/console/users?limit=abc",
		"/api/admin/console/orgs?sort=owner_id",
		"/api/admin/console/orgs?tier=gold",
		"/api/admin/console/orgs?ownerStatus=banned",
		"/api/admin/console/orgs?offset=-1",
	} {
		if w := consoleCall(router, http.MethodGet, path, ""); w.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400 (%s)", path, w.Code, w.Body.String())
		}
	}
}

func TestAdminConsole_ListsAreRefusedToEveryoneButAPlatformAdmin(t *testing.T) {
	for _, role := range []string{"user", "platform_viewer", "platform_operator", ""} {
		router := consoleRouter(role, &fakeConsole{}, &fakeSuspender{})
		for _, path := range []string{"/api/admin/console/users?sort=email&limit=5", "/api/admin/console/orgs?sort=name&limit=5"} {
			if w := consoleCall(router, http.MethodGet, path, ""); w.Code != http.StatusForbidden {
				t.Errorf("%s as %q = %d, want 403", path, role, w.Code)
			}
		}
	}
}
