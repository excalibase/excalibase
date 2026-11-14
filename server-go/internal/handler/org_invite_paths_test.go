package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

type failingUserStore struct{ emailUserStore }

func (s *failingUserStore) FindUserByID(context.Context, string) (*domain.User, error) {
	return nil, errors.New("db down")
}

func TestInviteByUserId_StoreFailuresAreRefused(t *testing.T) {
	orgs := &memberOrgStore{}
	w := httptest.NewRecorder()
	(&OrgHandler{orgStore: orgs}).resolveAndAddMember(w, inviteRequest(), "org1", "u-1", "", "viewer")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("no user store: got %d, want 500", w.Code)
	}
	w = httptest.NewRecorder()
	NewOrgHandler(orgs, &failingUserStore{}).resolveAndAddMember(w, inviteRequest(), "org1", "u-1", "", "viewer")
	if w.Code != http.StatusInternalServerError || orgs.invites != 0 {
		t.Fatalf("lookup failure: got %d with %d invites, want 500 and none", w.Code, orgs.invites)
	}
}

func TestIsValidProjectRole(t *testing.T) {
	for role, want := range map[string]bool{
		domain.ProjectRoleAdmin: true, domain.ProjectRoleEditor: true, domain.ProjectRoleViewer: true,
		"superuser": false, "": false, "owner": false,
	} {
		if got := isValidProjectRole(role); got != want {
			t.Errorf("isValidProjectRole(%q) = %v, want %v", role, got, want)
		}
	}
}

func TestAuthHookUpdate_RefusesTheInstallsOwnAddresses(t *testing.T) {
	h := &ProvisioningHandler{}
	h.SetAuthHookDeny([]netip.Prefix{netip.MustParsePrefix("93.184.216.0/28")})
	for url, want := range map[string]int{"https://93.184.216.4/hook": http.StatusBadRequest, "https://hooks.example.com/hook": http.StatusOK} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"enabled":false,"url":"`+url+`"}`))
		_, ok := decodeAuthHookUpdate(rec, req, "before-user-created", h.authHookDeny)
		if (want == http.StatusOK) != ok {
			t.Errorf("%s: ok=%v, want status %d (got %d)", url, ok, want, rec.Code)
		}
	}
}
