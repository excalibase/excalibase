package handler

import (
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
)

// ProjectRightsHandler tells a caller what they may do on a project, so a tool
// can refuse before asking for a confirmation that the delete would then fail.
type ProjectRightsHandler struct{}

// Get answers from the access RequireProjectAccess resolved, with the same role
// test the delete routes run (RequireProjectRole) and the token's write scope.
// The route sits behind that gate, so a project the caller cannot see is a 404
// before this runs; a request without a resolved access is answered the same.
func (ProjectRightsHandler) Get(w http.ResponseWriter, r *http.Request) {
	access := custommw.ProjectAccessFromContext(r.Context())
	if access == nil {
		httpError(w, "project not found", http.StatusNotFound)
		return
	}
	writes := auth.TokenAllowsMethod(auth.GetToken(r.Context()), http.MethodDelete)
	role := ""
	switch {
	case access.PlatformAdmin:
		role = "platform_admin"
	case access.Member != nil:
		role = access.Member.Role
	}
	writeJSON(w, map[string]any{
		"role":          role,
		"canDelete":     writes && access.RoleAtLeast(domain.OrgRoleDeveloper),
		"canAdminister": writes && access.RoleAtLeast(domain.OrgRoleAdmin),
	})
}
