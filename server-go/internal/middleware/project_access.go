package middleware

import (
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

// RequireProjectAccess gates per-project handlers behind an org-membership
// check. Without this, any authenticated user who knows or guesses a
// projectID can call /api/provision/{projectId}/credentials and similar
// endpoints — there is no per-resource ownership check elsewhere on the
// happy path. Platform admins (PermManageUsers) bypass the check; everyone
// else must be a member of the project's org.
//
// Mount sequence: auth.RequireAuth → TenantContext → RequireProjectAccess.
// Returns 401 if unauthenticated, 404 if the project doesn't exist (avoids
// confirming existence to non-members), 403 otherwise.
func RequireProjectAccess(instStore storage.InstanceStore, orgStore storage.OrgStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			projectID := chi.URLParam(r, "projectId")
			if projectID == "" {
				next.ServeHTTP(w, r)
				return
			}
			if !checkProjectAccess(w, r, projectID, instStore, orgStore) {
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// checkProjectAccess performs the auth/membership check and writes error
// responses. Returns true if access is granted.
func checkProjectAccess(w http.ResponseWriter, r *http.Request, projectID string, instStore storage.InstanceStore, orgStore storage.OrgStore) bool {
	user := auth.GetUser(r.Context())
	if user == nil {
		http.Error(w, `{"error":"unauthenticated"}`, http.StatusUnauthorized)
		return false
	}
	// Platform admins bypass the per-project gate — needed for support and incident response.
	if auth.HasPermission(user.Role, auth.PermManageUsers) {
		return true
	}
	inst, err := instStore.FindByProjectID(projectID)
	if err != nil || inst == nil {
		http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
		return false
	}
	if inst.OrgID == "" || orgStore == nil {
		// Cannot verify membership → fail closed.
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return false
	}
	member, err := orgStore.GetOrgMember(r.Context(), inst.OrgID, user.ID)
	if err != nil || member == nil {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return false
	}
	return true
}
