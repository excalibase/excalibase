package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

type matrixAudit struct{}

func (matrixAudit) ListAudit(context.Context, adminconsole.AuditQuery) (adminconsole.AuditPage, error) {
	return adminconsole.AuditPage{Items: []adminconsole.AuditRow{}}, nil
}

func (matrixAudit) ReadUserDetail(context.Context, string) (*adminconsole.UserDetail, error) {
	return &adminconsole.UserDetail{}, nil
}

var adminAuditPaths = []string{"/api/admin/audit/", "/api/admin/accounts/u-1"}

func matrixBody(router http.Handler, path, token string) (int, string) {
	answer := matrixAnswer(router, http.MethodGet, path, token)
	return answer.code, answer.body
}

// matrixReply is everything a caller can tell one answer from another by.
type matrixReply struct {
	code        int
	contentType string
	body        string
}

func matrixAnswer(router http.Handler, method, path, token string) matrixReply {
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return matrixReply{w.Code, w.Header().Get("Content-Type"), w.Body.String()}
}

const (
	matrixOperatorID = "u-operator"
	matrixViewerID   = "u-viewer"
	callerOperator   = "operator"
	callerViewer     = "viewer"
)

// withStaffRoles gives the platform two more signed-in sessions: an operator and a viewer.
func withStaffRoles(deps *handlerDeps, platform *fakePlatform) {
	for id, role := range map[string]string{matrixOperatorID: "platform_operator", matrixViewerID: "platform_viewer"} {
		platform.Users[id] = &domain.User{ID: id, Role: role, Active: true}
		raw := testutil.FixtureToken(id)
		hash := auth.HashToken(raw)
		platform.ByHash[hash] = &domain.AccessToken{TokenHash: hash, UserID: id, Scopes: auth.ScopeSession}
	}
}

func staffSessionToken(name string) string { return testutil.FixtureToken(name) }

func TestAdminAudit_RoutesAreStaffOnlyAndOtherwiseLookUnmounted(t *testing.T) {
	router, who, admins := buildMatrixWith(t, config.AppConfig{DeploymentMode: "selfhosted"}, func(deps *handlerDeps) {
		deps.adminAudit = handler.NewAdminAuditHandler(adminconsole.NewAuditLog(matrixAudit{}), adminconsole.NewUserDetails(matrixAudit{}, nil))
	})
	for _, path := range adminAuditPaths {
		if code, _ := matrixBody(router, path, admins[callerAdminSession]); code != http.StatusOK {
			t.Errorf("%s as the admin's session = %d, want 200", path, code)
		}
	}
	others := map[string]string{
		callerAdminReadOnly: admins[callerAdminReadOnly], callerAdminWrite: admins[callerAdminWrite],
		callerDeveloper: who[callerDeveloper], callerOtherOrg: who[callerOtherOrg],
	}
	for name, token := range others {
		wantCode, wantBody := matrixBody(router, "/api/admin/nothing-mounted-here", token)
		for _, path := range adminAuditPaths {
			if code, body := matrixBody(router, path, token); code != wantCode || body != wantBody {
				t.Errorf("%s on %s = %d %q, want the unmounted %d %q", name, path, code, body, wantCode, wantBody)
			}
		}
	}
	for _, path := range adminAuditPaths {
		if code, _ := matrixBody(router, path, ""); code != http.StatusUnauthorized {
			t.Errorf("%s anonymous = %d, want 401", path, code)
		}
	}
}

func TestAdminAudit_NotWiredMeansNotMounted(t *testing.T) {
	router, _, admins := buildMatrixWith(t, config.AppConfig{DeploymentMode: "selfhosted"}, func(deps *handlerDeps) { deps.adminAudit = nil })
	for _, path := range adminAuditPaths {
		if code, _ := matrixBody(router, path, admins[callerAdminSession]); code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", path, code)
		}
	}
}

// Whatever the caller sends, whoever is not a platform admin's session learns
// nothing: the answer matches an unmounted route in status, headers and body,
// for every method, including the writes a token gate would otherwise refuse first.
func TestAdminAudit_NoMethodOrCredentialSeparatesTheRoutesFromAnUnmountedOne(t *testing.T) {
	router, who, admins, _ := buildMatrixPlatform(t, config.AppConfig{DeploymentMode: "selfhosted"}, func(deps *handlerDeps, platform *fakePlatform) {
		withStaffRoles(deps, platform)
	})
	tokens := map[string]string{
		callerAdminReadOnly: admins[callerAdminReadOnly], callerAdminWrite: admins[callerAdminWrite],
		callerDeveloper: who[callerDeveloper], callerOtherOrg: who[callerOtherOrg],
		callerBoundElsewhere: who[callerBoundElsewhere], callerReadOnly: who[callerReadOnly],
		callerOperator: staffSessionToken(matrixOperatorID), callerViewer: staffSessionToken(matrixViewerID),
	}
	methods := []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	for name, token := range tokens {
		for _, method := range methods {
			want := matrixAnswer(router, method, "/api/admin/zz", token)
			for _, path := range adminAuditPaths {
				if got := matrixAnswer(router, method, path, token); got != want {
					t.Errorf("%s %s as %s = %+v, want the unmounted %+v", method, path, name, got, want)
				}
			}
		}
	}
}
