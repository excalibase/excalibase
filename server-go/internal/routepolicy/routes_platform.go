package routepolicy

import (
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

const (
	parameterGroupRoute = "/api/parameter-groups/{name}"
	vaultSecretsRoute   = "/api/vault/secrets/*"
)

// get, post and so on keep the table itself readable — the rows are data, and
// a row should fit on one line's worth of thought.
var (
	get     = []string{http.MethodGet}
	post    = []string{http.MethodPost}
	put     = []string{http.MethodPut}
	patch   = []string{http.MethodPatch}
	del     = []string{http.MethodDelete}
	anyVerb = []string{MethodAll}
)

const (
	permManageUsers = string(auth.PermManageUsers)
	permManageSetup = string(auth.PermManageSetup)
	permManageOrgs  = string(auth.PermManageOrgs)
	permViewAny     = string(auth.PermViewAny)
	permDelete      = string(auth.PermDelete)
	permCredentials = string(auth.PermViewCredentials)

	roleViewer    = domain.OrgRoleViewer
	roleDeveloper = domain.OrgRoleDeveloper
	roleAdmin     = domain.OrgRoleAdmin
	roleOwner     = domain.OrgRoleOwner
)

// publicRows take no credential. Each one is public on purpose; the note says
// why, because "no auth" is the single most expensive thing to get wrong.
var publicRows = []Row{
	{Methods: get, Pattern: "/healthz", Auth: AuthPublic, Note: "liveness probe"},
	{Methods: get, Pattern: "/api/config", Auth: AuthPublic, Note: "deployment mode only; the studio reads it before login"},
	{Methods: anyVerb, Pattern: "/metrics", Auth: AuthPublic, Note: "Prometheus scrape target; network-restricted, not credential-restricted"},

	{Methods: post, Pattern: "/api/auth/register", Auth: AuthPublic, Note: "rate-limited per IP; invite-only mode gates it in the handler"},
	{Methods: post, Pattern: "/api/auth/login", Auth: AuthPublic},
	{Methods: get, Pattern: "/api/auth/setup-status", Auth: AuthPublic, Note: "first-run wizard polls it before any user exists"},
	{Methods: post, Pattern: "/api/auth/logout", Auth: AuthPublic, Note: "clearing a cookie needs no proof of who owned it"},

	{Methods: get, Pattern: "/api/vault/status", Auth: AuthPublic, Note: "sealed/initialized only; the first-run wizard polls it before any user exists"},
	{Methods: get, Pattern: "/api/vault/pki/public-key", Auth: AuthPublic, Note: "public by definition"},

	{Methods: post, Pattern: "/api/email/verify/confirm", Auth: AuthPublic, Note: "the emailed token is the credential"},
	{Methods: post, Pattern: "/api/email/reset/send", Auth: AuthPublic, Note: "a password reset is requested by someone who cannot log in"},
	{Methods: post, Pattern: "/api/email/reset/confirm", Auth: AuthPublic, Note: "the emailed token is the credential"},

	{Methods: get, Pattern: "/api/setup/status", Auth: AuthPublic, Note: "the installer polls it before any credential exists; it answers only whether setup is complete"},
}

// platformRows are the platform-wide surfaces: they name no tenant, so a
// platform permission is the whole of their authorization.
var platformRows = []Row{
	{Methods: get, Pattern: "/api/capacity", Auth: AuthSession, Owner: OwnerNone},
	{Methods: post, Pattern: "/api/email/verify/send", Auth: AuthSession, Owner: OwnerNone, Note: "mails only the caller's own verified-address flow, so a read-only token may not trigger a send"},
	{Methods: get, Pattern: "/api/tiers/", Auth: AuthSession, Owner: OwnerNone, Note: "tier specs feed the provision page's selector; editing lives under /api/admin/tiers"},
	{Methods: get, Pattern: "/api/postgres/catalog", Auth: AuthSession, Owner: OwnerNone, Note: "the supported PostgreSQL majors and their DocumentDB availability; the create-project form reads it instead of carrying its own copy"},
	{Methods: get, Pattern: "/api/alerts/", Auth: AuthSession, Owner: OwnerNone, Note: "the handler scopes the list to the caller's own projects; PermViewAny sees every tenant's"},
	{Methods: get, Pattern: "/api/alerts/history", Auth: AuthSession, Owner: OwnerNone, Note: "scoped like the active list"},
	{Methods: post, Pattern: "/api/setup/install/{databaseType}", Auth: AuthSession, Permission: permManageSetup, Owner: OwnerPlatformRole, Note: "applies a remote YAML cluster-wide"},

	{Methods: get, Pattern: "/api/parameter-groups/", Auth: AuthSession, Permission: permViewAny, Owner: OwnerPlatformRole, Note: "cluster-wide configuration no tenant-facing surface reads"},
	{Methods: get, Pattern: parameterGroupRoute, Auth: AuthSession, Permission: permViewAny, Owner: OwnerPlatformRole},
	{Methods: post, Pattern: "/api/parameter-groups/", Auth: AuthSession, Permission: permManageSetup, Owner: OwnerPlatformRole, Note: "parameter groups are cluster-wide configuration"},
	{Methods: put, Pattern: parameterGroupRoute, Auth: AuthSession, Permission: permManageSetup, Owner: OwnerPlatformRole},
	{Methods: del, Pattern: parameterGroupRoute, Auth: AuthSession, Permission: permManageSetup, Owner: OwnerPlatformRole},

	{Methods: get, Pattern: "/api/auth/me", Auth: AuthSession, Owner: OwnerNone, Capability: "self:read", Note: "every credential may read its own identity"},
	{Methods: get, Pattern: "/api/auth/users/", Auth: AuthSession, Permission: permManageUsers, Owner: OwnerPlatformRole},
	{Methods: post, Pattern: "/api/auth/users/", Auth: AuthSession, Permission: permManageUsers, Owner: OwnerPlatformRole},
	{Methods: del, Pattern: "/api/auth/users/{userId}", Auth: AuthSession, Permission: permManageUsers, Owner: OwnerPlatformRole},

	{Methods: get, Pattern: "/api/auth/tokens/", Auth: AuthSession, Owner: OwnerTokenSubject, Note: "lists only the caller's own tokens"},
	{Methods: post, Pattern: "/api/auth/tokens/", Auth: AuthSession, Owner: OwnerTokenSubject, Note: "a narrowed PAT may only mint a subset of its own scopes"},
	{Methods: del, Pattern: "/api/auth/tokens/{tokenHash}", Auth: AuthSession, Owner: OwnerTokenSubject},
	{Methods: post, Pattern: "/api/auth/tokens/{tokenHash}/rotate", Auth: AuthSession, Owner: OwnerTokenSubject},

	{Methods: get, Pattern: "/api/admin/projects", Auth: AuthSession, Permission: permViewAny, Owner: OwnerPlatformRole},
	{Methods: get, Pattern: "/api/admin/logs", Auth: AuthSession, Permission: permViewAny, Owner: OwnerPlatformRole},
	{Methods: del, Pattern: "/api/admin/projects/{projectId}", Auth: AuthSession, Permission: permDelete, Owner: OwnerPlatformRole, Unrestricted: true, Note: "cross-tenant force drop; a non-admin is refused before the project id is looked at, and a narrowed PAT may not destroy a tenant it was never scoped to"},
	{Methods: del, Pattern: "/api/admin/orgs/{orgId}", Auth: AuthSession, Permission: permManageOrgs, Owner: OwnerPlatformRole},
	{Methods: get, Pattern: "/api/admin/tiers/", Auth: AuthSession, Permission: permViewAny, Owner: OwnerPlatformRole},
	{Methods: put, Pattern: "/api/admin/tiers/{tier}", Auth: AuthSession, Permission: permManageSetup, Owner: OwnerPlatformRole},
	{Methods: get, Pattern: "/api/admin/service-accounts/", Auth: AuthSession, Permission: permManageUsers, Owner: OwnerPlatformRole},
	{Methods: get, Pattern: "/api/admin/service-accounts/{name}/tokens", Auth: AuthSession, Permission: permManageUsers, Owner: OwnerPlatformRole},
	{Methods: post, Pattern: "/api/admin/service-accounts/", Auth: AuthSession, Permission: permManageUsers, Owner: OwnerPlatformRole, Unrestricted: true, Note: "minting a service credential from a narrowed PAT would launder it into a wider one"},
	{Methods: del, Pattern: "/api/admin/service-accounts/{name}", Auth: AuthSession, Permission: permManageUsers, Owner: OwnerPlatformRole, Unrestricted: true},
}

// vaultRows guard the platform's key material. Reads are open to the service
// principals that need one named secret; everything that creates, destroys or
// overwrites key material additionally refuses any narrowed PAT, so a
// project-bound credential can never reach platform-wide authority.
var vaultRows = []Row{
	{Methods: post, Pattern: "/api/vault/init", Auth: AuthSession, Permission: permCredentials, Owner: OwnerPlatformRole, Unrestricted: true, Note: "returns the unseal shares"},
	{Methods: post, Pattern: "/api/vault/unseal", Auth: AuthSession, Permission: permCredentials, Owner: OwnerPlatformRole, Unrestricted: true},
	{Methods: post, Pattern: "/api/vault/seal", Auth: AuthSession, Permission: permCredentials, Owner: OwnerPlatformRole, Unrestricted: true},
	{Methods: post, Pattern: "/api/vault/rekey", Auth: AuthSession, Permission: permCredentials, Owner: OwnerPlatformRole, Unrestricted: true},
	{Methods: get, Pattern: "/api/vault/secrets-list", Auth: AuthSession, Permission: permCredentials, Owner: OwnerPlatformRole, Unrestricted: true, Note: "a prefix spanning projects inventories every tenant; a prefix naming one is bound to it like any other project read"},
	{Methods: del, Pattern: "/api/vault/secrets-list", Auth: AuthSession, Permission: permCredentials, Owner: OwnerPlatformRole, Unrestricted: true, Note: "deletes an entire secret prefix"},
	{Methods: get, Pattern: vaultSecretsRoute, Auth: AuthSession, Permission: permCredentials, Owner: OwnerPlatformRole, Capability: "vault:read:<secret path>", Note: "the capability's selector names the one secret the service may read; a human caller is additionally bound to the project the path names, and every read is audited"},
	{Methods: put, Pattern: vaultSecretsRoute, Auth: AuthSession, Permission: permCredentials, Owner: OwnerPlatformRole, Unrestricted: true},
	{Methods: del, Pattern: vaultSecretsRoute, Auth: AuthSession, Permission: permCredentials, Owner: OwnerPlatformRole, Unrestricted: true},
}

// orgRows are the team surfaces. Reads bind to membership and hide the org
// behind a 404; writes require the org admin rung, owner for deleting the org.
var orgRows = []Row{
	{Methods: get, Pattern: "/api/orgs/", Auth: AuthSession, Owner: OwnerNone, Note: "lists only the caller's own orgs"},
	{Methods: post, Pattern: "/api/orgs/", Auth: AuthSession, Owner: OwnerNone, Note: "cloud only; the creator becomes its owner"},
	{Methods: get, Pattern: "/api/orgs/{orgId}/", Auth: AuthSession, Param: ParamOrg, Owner: OwnerOrgMembership},
	{Methods: patch, Pattern: "/api/orgs/{orgId}/", Auth: AuthSession, Param: ParamOrg, Owner: OwnerOrgMembership, MinRole: roleAdmin},
	{Methods: del, Pattern: "/api/orgs/{orgId}/", Auth: AuthSession, Param: ParamOrg, Owner: OwnerOrgMembership, MinRole: roleOwner, Note: "cloud only"},
	{Methods: get, Pattern: "/api/orgs/{orgId}/invites", Auth: AuthSession, Param: ParamOrg, Owner: OwnerOrgMembership},
	{Methods: get, Pattern: "/api/orgs/{orgId}/members/", Auth: AuthSession, Param: ParamOrg, Owner: OwnerOrgMembership},
	{Methods: post, Pattern: "/api/orgs/{orgId}/members/", Auth: AuthSession, Param: ParamOrg, Owner: OwnerOrgMembership, MinRole: roleAdmin},
	{Methods: patch, Pattern: "/api/orgs/{orgId}/members/{userId}", Auth: AuthSession, Param: ParamOrg, Owner: OwnerOrgMembership, MinRole: roleAdmin},
	{Methods: del, Pattern: "/api/orgs/{orgId}/members/{userId}", Auth: AuthSession, Param: ParamOrg, Owner: OwnerOrgMembership, MinRole: roleAdmin},
	{Methods: get, Pattern: "/api/orgs/{orgId}/projects/{projectId}/members/", Auth: AuthSession, Param: ParamOrg, Owner: OwnerOrgMembership, Note: "{projectId} must belong to {orgId}; this route does not run RequireProjectAccess"},
	{Methods: post, Pattern: "/api/orgs/{orgId}/projects/{projectId}/members/", Auth: AuthSession, Param: ParamOrg, Owner: OwnerOrgMembership, MinRole: roleAdmin, Note: "{projectId} must belong to {orgId}"},
	{Methods: patch, Pattern: "/api/orgs/{orgId}/projects/{projectId}/members/{userId}", Auth: AuthSession, Param: ParamOrg, Owner: OwnerOrgMembership, MinRole: roleAdmin, Note: "{projectId} must belong to {orgId}"},
	{Methods: del, Pattern: "/api/orgs/{orgId}/projects/{projectId}/members/{userId}", Auth: AuthSession, Param: ParamOrg, Owner: OwnerOrgMembership, MinRole: roleAdmin, Note: "{projectId} must belong to {orgId}"},
}
