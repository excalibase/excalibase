package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
)

// Minting a credential is Admin-only; reading the list is open to members.
func TestRequireCredentialPermission(t *testing.T) {
	reached := false
	gated := RequireCredentialPermission(PermCreateServiceKey)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
	}))
	cases := []struct {
		name   string
		method string
		role   string
		want   int
		reach  bool
	}{
		{"developer cannot mint", http.MethodPost, domain.OrgRoleDeveloper, http.StatusForbidden, false},
		{"viewer cannot mint", http.MethodDelete, domain.OrgRoleViewer, http.StatusForbidden, false},
		{"no project access cannot mint", http.MethodPost, "", http.StatusForbidden, false},
		{"admin can mint", http.MethodPost, domain.OrgRoleAdmin, http.StatusOK, true},
		{"developer can list", http.MethodGet, domain.OrgRoleDeveloper, http.StatusOK, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached = false
			req := httptest.NewRequest(tc.method, "/keys", nil)
			if tc.role != "" {
				access := &custommw.ProjectAccess{Member: &domain.OrgMember{Role: tc.role}}
				req = req.WithContext(custommw.WithProjectAccess(req.Context(), access))
			}
			w := httptest.NewRecorder()
			gated.ServeHTTP(w, req)
			if w.Code != tc.want || reached != tc.reach {
				t.Fatalf("got %d reached=%v, want %d reached=%v", w.Code, reached, tc.want, tc.reach)
			}
		})
	}
}

func TestCallerMayRefusesAnUnknownPermission(t *testing.T) {
	access := &custommw.ProjectAccess{PlatformAdmin: true}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = req.WithContext(custommw.WithProjectAccess(req.Context(), access))
	if callerMay(req, CredentialPermission("database:Unknown")) {
		t.Fatal("an unnamed permission must never be granted")
	}
}

func TestGetOwnerDBRefusesAnInvalidProjectID(t *testing.T) {
	if _, err := (&SchemaHandler{}).getOwnerDB("../etc"); err == nil {
		t.Fatal("an invalid project id reached the owner pool")
	}
}
