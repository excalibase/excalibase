package middleware

import (
	"context"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

const (
	projectAccessKey contextKey = "project_access"

	errBodyUnauthenticated = `{"error":"unauthenticated"}`
	errBodyProjectNotFound = `{"error":"project not found"}`
	errBodyScope           = `{"error":"token lacks required scope: write"}`
	errBodyRole            = `{"error":"insufficient project role"}`
)

// servingAllows reports whether a request may proceed against the resolved
// project. A project the platform must not serve keeps only the surface that
// lets its owner see what is wrong and get rid of it: reading the status,
// retrying the DELETE, and retrying an outstanding backup purge.
//
// Two states qualify (domain.IsNotServable). Under teardown, every other
// operation would act on resources that are going away — a rotated credential
// on a database about to vanish, a function deployed into a namespace being
// removed, a policy row that outlives the project it names. In RESTORING, the
// row exists with working-looking credentials for a database no probe has
// confirmed; serving it would route traffic and mint tokens for a recovery
// that may never have happened. DELETE stays open in both, which is what
// makes a restore abandoned by a dead process recoverable at all.
//
// The rule lives here, in the one place that already resolves the project, so
// no handler can be added later that forgets it.
func servingAllows(r *http.Request, inst *domain.DatabaseInstance) bool {
	if inst == nil || !domain.IsNotServable(inst.Status) {
		return true
	}
	id := inst.ProjectID
	switch r.URL.Path {
	case "/api/provision/" + id, "/api/provision/" + id + "/":
		return r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodDelete
	case "/api/provision/" + id + "/backups/purge":
		return r.Method == http.MethodPost
	case "/api/projects/" + id + "/info", "/api/projects/" + id + "/info/":
		// The data plane's read. Its handler answers 404 for a project it
		// must not serve — stricter than this gate, and unambiguous for a
		// caller that must stop serving the project rather than retry.
		return r.Method == http.MethodGet
	default:
		return false
	}
}

// notServableBody renders the refusal as the JSON shape every other gate
// error uses.
func notServableBody(status string) string {
	return `{"error":"` + domain.NotServableReason(status) + `"}`
}

// instanceOrLookup returns the resolved project row, fetching it for platform
// admins whose access carries no membership row.
func (a *ProjectAccess) instanceOrLookup(instStore storage.InstanceStore) *domain.DatabaseInstance {
	if a.Instance != nil {
		return a.Instance
	}
	if !a.PlatformAdmin || instStore == nil {
		return nil
	}
	inst, err := instStore.FindByProjectID(a.projectID)
	if err != nil {
		return nil
	}
	return inst
}

// ProjectAccess is the outcome of binding the path project to the caller.
// PlatformAdmin callers carry no membership row; everyone else does.
type ProjectAccess struct {
	Instance      *domain.DatabaseInstance
	Member        *domain.OrgMember
	PlatformAdmin bool
	// projectID is the path project this access was resolved for. A platform
	// admin carries no Instance, so the deletion gate looks the row up by it.
	projectID string
}

// RoleAtLeast reports whether the caller holds at least minRole on the
// project's org. Platform admins satisfy every role.
func (a *ProjectAccess) RoleAtLeast(minRole string) bool {
	if a.PlatformAdmin {
		return true
	}
	return a.Member != nil && auth.OrgRoleAtLeast(a.Member.Role, minRole)
}

// ProjectAccessFromContext returns the access resolved by RequireProjectAccess
// for this request, or nil when the gate did not run.
func ProjectAccessFromContext(ctx context.Context) *ProjectAccess {
	access, _ := ctx.Value(projectAccessKey).(*ProjectAccess)
	return access
}

// ResolveProjectAccess binds projectID to the caller. It returns nil when the
// project must not be visible to the caller: unknown project, project without
// an org, caller not a member, or the caller's token is bound to a different
// project. Platform admins (PermManageUsers) skip the membership lookup but
// never escape a token binding.
//
// A project in DELETING or RESTORING is deliberately still resolved: its
// owner has to be able to read the stalled operation and retry the DELETE.
// The data plane is where such a project stops being served — see
// GetProjectInfo.
func ResolveProjectAccess(ctx context.Context, user *domain.User, token *domain.AccessToken, projectID string, instStore storage.InstanceStore, orgStore storage.OrgStore) *ProjectAccess {
	if user == nil || !auth.TokenBoundToProject(token, projectID) {
		return nil
	}
	if auth.HasPermission(user.Role, auth.PermManageUsers) {
		return &ProjectAccess{PlatformAdmin: true, projectID: projectID}
	}
	inst, err := instStore.FindByProjectID(projectID)
	if err != nil || inst == nil || inst.OrgID == "" || orgStore == nil {
		return nil
	}
	member, err := orgStore.GetOrgMember(ctx, inst.OrgID, user.ID)
	if err != nil || member == nil {
		return nil
	}
	return &ProjectAccess{Instance: inst, Member: member, projectID: projectID}
}

// RequireProjectAccess binds the {projectId} in the path to the caller before
// the handler runs: session users must be a member of the project's org, and
// a PAT bound to a project may not leave it. Anything the caller may not see
// is a 404 — never a 403, which would confirm the project exists. A token
// whose scopes do not allow the method (read-only PAT on a write) is a 403.
//
// Mount sequence: auth.RequireAuth → TenantContext → RequireProjectAccess.
// The resolved access is cached on the context so RequireProjectRole does not
// query membership a second time.
func RequireProjectAccess(instStore storage.InstanceStore, orgStore storage.OrgStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if chi.URLParam(r, "projectId") == "" {
				next.ServeHTTP(w, r)
				return
			}
			access, ok := gateProjectAccess(w, r, instStore, orgStore)
			if !ok {
				return
			}
			if inst := access.instanceOrLookup(instStore); !servingAllows(r, inst) {
				http.Error(w, notServableBody(inst.Status), http.StatusConflict)
				return
			}
			if !auth.TokenAllowsMethod(auth.GetToken(r.Context()), r.Method) {
				http.Error(w, errBodyScope, http.StatusForbidden)
				return
			}
			ctx := context.WithValue(r.Context(), projectAccessKey, access)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireProjectRole gates a per-project route behind a MINIMUM org role
// (Owner ⊇ Admin ⊇ Developer ⊇ Viewer). Layer it on top of RequireProjectAccess
// for routes that need more than bare membership — e.g. destructive lifecycle
// and credential reads require Admin, schema/data writes require Developer.
// 401/404 like RequireProjectAccess, plus 403 when the role is below minRole.
func RequireProjectRole(minRole string, instStore storage.InstanceStore, orgStore storage.OrgStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !gateProjectRole(w, r, minRole, instStore, orgStore) {
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireProjectRoleForWrites is a method-aware variant: safe GET/HEAD reads
// pass on bare membership, but any mutating method (POST/PUT/PATCH/DELETE)
// requires the minimum org role. Used on the schema subtree so a Viewer can
// browse tables/rows but only a Developer can run DDL, /query, or row writes.
func RequireProjectRoleForWrites(minRole string, instStore storage.InstanceStore, orgStore storage.OrgStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				next.ServeHTTP(w, r)
				return
			}
			if !gateProjectRole(w, r, minRole, instStore, orgStore) {
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// gateProjectAccess resolves access (reusing a cached resolution when the
// request already passed RequireProjectAccess) and writes 401/404 on refusal.
func gateProjectAccess(w http.ResponseWriter, r *http.Request, instStore storage.InstanceStore, orgStore storage.OrgStore) (*ProjectAccess, bool) {
	ctx := r.Context()
	user := auth.GetUser(ctx)
	if user == nil {
		http.Error(w, errBodyUnauthenticated, http.StatusUnauthorized)
		return nil, false
	}
	access := ProjectAccessFromContext(ctx)
	if access == nil {
		access = ResolveProjectAccess(ctx, user, auth.GetToken(ctx), chi.URLParam(r, "projectId"), instStore, orgStore)
	}
	if access == nil {
		http.Error(w, errBodyProjectNotFound, http.StatusNotFound)
		return nil, false
	}
	return access, true
}

// gateProjectRole enforces the minimum org role on top of gateProjectAccess.
func gateProjectRole(w http.ResponseWriter, r *http.Request, minRole string, instStore storage.InstanceStore, orgStore storage.OrgStore) bool {
	access, ok := gateProjectAccess(w, r, instStore, orgStore)
	if !ok {
		return false
	}
	if !access.RoleAtLeast(minRole) {
		http.Error(w, errBodyRole, http.StatusForbidden)
		return false
	}
	return true
}
