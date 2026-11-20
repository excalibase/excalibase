package handler

import (
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/domain"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
)

// CredentialPermission names a call that mints a credential granting more than
// a Developer's own rights: it bypasses row-level security or gives raw
// database or storage access. No credential may do more than the identity that
// creates it, so until IAM-style policies land (EXC-444, EXC-763) each is held
// by the Admin role alone, and every check goes through callerMay so a policy
// can grant one later without touching the handlers.
type CredentialPermission string

const (
	PermCreateServiceKey CredentialPermission = "database:CreateServiceKey"
	PermCreateLoginRole  CredentialPermission = "database:CreateLoginRole"
	PermCreateAccessKey  CredentialPermission = "storage:CreateAccessKey"
)

// errCredentialAdminOnly is the refusal for a call that mints a credential
// above the caller's own rung.
const errCredentialAdminOnly = "creating this credential needs the project Admin role"

// callerMay reports whether the caller holds perm on the path project. An
// unresolved access reads as no.
func callerMay(r *http.Request, perm CredentialPermission) bool {
	access := custommw.ProjectAccessFromContext(r.Context())
	if access == nil {
		return false
	}
	switch perm {
	case PermCreateServiceKey, PermCreateLoginRole, PermCreateAccessKey:
		return access.RoleAtLeast(domain.OrgRoleAdmin) // Admin-only until EXC-444 grants these by policy
	default:
		return false
	}
}

// RequireCredentialPermission refuses a write unless the caller holds perm.
// Reads pass: listing keys shows prefixes and dates, never a secret.
func RequireCredentialPermission(perm CredentialPermission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead && !callerMay(r, perm) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"insufficient project role"}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// callerMayReadAppValues reports whether the caller may read what an app's
// plain variables hold: Developer and above, never a Viewer.
func callerMayReadAppValues(r *http.Request) bool {
	access := custommw.ProjectAccessFromContext(r.Context())
	return access != nil && access.RoleAtLeast(domain.OrgRoleDeveloper)
}
