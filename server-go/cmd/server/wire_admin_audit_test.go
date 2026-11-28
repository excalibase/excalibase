package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/handler"
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
	req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

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
