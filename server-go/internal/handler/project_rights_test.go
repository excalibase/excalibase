package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
)

func rightsFor(t *testing.T, access *custommw.ProjectAccess, scopes string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/proj-a/access", nil)
	ctx := custommw.WithProjectAccess(req.Context(), access)
	ctx = auth.SetToken(ctx, &domain.AccessToken{Name: "laptop", Scopes: scopes})
	w := httptest.NewRecorder()
	(&ProjectRightsHandler{}).Get(w, req.WithContext(ctx))
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body
}

func memberAs(role string) *custommw.ProjectAccess {
	return &custommw.ProjectAccess{Member: &domain.OrgMember{Role: role}}
}

func TestProjectRightsFollowTheRoleLadder(t *testing.T) {
	cases := []struct {
		name                     string
		access                   *custommw.ProjectAccess
		role                     string
		canDelete, canAdminister bool
	}{
		{"viewer", memberAs(domain.OrgRoleViewer), domain.OrgRoleViewer, false, false},
		{"developer", memberAs(domain.OrgRoleDeveloper), domain.OrgRoleDeveloper, true, false},
		{"admin", memberAs(domain.OrgRoleAdmin), domain.OrgRoleAdmin, true, true},
		{"platform admin", &custommw.ProjectAccess{PlatformAdmin: true}, "platform_admin", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := rightsFor(t, tc.access, "")
			if code != http.StatusOK || body["role"] != tc.role || body["canDelete"] != tc.canDelete || body["canAdminister"] != tc.canAdminister {
				t.Fatalf("got %d %v, want role %s canDelete %v canAdminister %v", code, body, tc.role, tc.canDelete, tc.canAdminister)
			}
		})
	}
}

func TestProjectRightsOfAReadOnlyTokenAreNone(t *testing.T) {
	code, body := rightsFor(t, memberAs(domain.OrgRoleAdmin), auth.ScopeRead)
	if code != http.StatusOK || body["canDelete"] != false || body["canAdminister"] != false {
		t.Fatalf("a read-only token on an admin: %d %v", code, body)
	}
}

// Without a resolved access the answer is the one an unknown project gets.
func TestProjectRightsWithoutResolvedAccessAreNotFound(t *testing.T) {
	if code, _ := rightsFor(t, nil, ""); code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", code)
	}
}
