// Package routepolicy is the control plane's route authorization table: one
// declarative row per mounted method + path pattern, stating who may call it
// and what resolves their right to the resource named in the path.
//
// It exists because the authorization bugs this platform has shipped were
// ABSENCES — a mount that enforced org membership but forgot the minimum role,
// a guard bound above the {projectId} segment where it is a silent no-op. No
// diff review surfaces a check that was never written. A table does: the
// coverage test in cmd/server walks the real router and fails when a mounted
// route has no row here, when a row here matches no mounted route, and when
// driving the real router with synthetic principals contradicts what the row
// declares.
//
// # The role ladder
//
// Every project-scoped route sits on one of three rungs. Adding a route means
// picking the rung it belongs to, and the rung is decided by what the call can
// destroy or disclose — never by which handler happens to own it.
//
//	viewer     Reads only. Project status and logs, metrics, performance,
//	           project info, schema browsing, listing buckets and objects,
//	           minting a download URL, listing realtime tables, alerts.
//
//	developer  Data-plane authoring: everything that changes what the tenant's
//	           database or its edge serves, and is undone by authoring it back.
//	           Schema DDL, /query and row writes, migrations, RLS and column
//	           policies, table grants, edge functions (deploy, secrets, egress),
//	           the browser-origin allowlist, auth settings, realtime publication
//	           membership, and storage buckets and objects. A bucket is a
//	           container inside the project the way a table is: dropping one
//	           sits on the same rung as DROP TABLE, not with project teardown.
//
//	admin      Project lifecycle and credentials — the operations a tenant
//	           cannot author their way back out of. Delete the project, read or
//	           rotate its database credentials, deletion protection, pause and
//	           resume, backups and restores, backup purge, full-database
//	           snapshots (a snapshot is a whole-database exfil).
//
//	owner      Only org deletion; everything else admin covers.
//
// Org-scoped routes read on membership and write on the org admin rung.
// Platform-wide routes (users, service accounts, tier configs, parameter
// groups, operator install, vault) carry a platform permission instead of an
// org role, and the credential-minting and key-material routes additionally
// refuse any narrowed PAT.
package routepolicy

// AuthClass names how a route establishes who is calling.
type AuthClass string

const (
	// AuthPublic takes no credential at all.
	AuthPublic AuthClass = "public"
	// AuthSession is auth.RequireAuth: a studio session or a PAT, with the
	// token's scopes enforced against the request method.
	AuthSession AuthClass = "session-or-token"
	// AuthHandlerSession is a route whose handler reads the authenticated
	// user itself without auth.RequireAuth above it — so an unauthenticated
	// caller is refused but the token's scopes are NOT checked.
	AuthHandlerSession AuthClass = "handler-session"
	// AuthRuntimeToken is the X-Excalibase-Runtime-Token shared secret,
	// derived per project so a token minted for one project cannot act on
	// another.
	AuthRuntimeToken AuthClass = "runtime-token"
	// AuthFunctionJWT is an end-user JWT whose audience must name the
	// project in the path.
	AuthFunctionJWT AuthClass = "function-jwt"
)

// Owner names what resolves the caller's right to the resource in the path.
type Owner string

const (
	// OwnerNone is a route that names no tenant resource.
	OwnerNone Owner = ""
	// OwnerProjectAccess is middleware.RequireProjectAccess, which binds
	// {projectId} to the caller and answers 404 for anything they may not see.
	OwnerProjectAccess Owner = "middleware.RequireProjectAccess"
	// OwnerOrgMembership is handler.OrgHandler resolving {orgId} against the
	// caller's membership (and {projectId} against the org, where present).
	OwnerOrgMembership Owner = "handler.OrgHandler org membership"
	// OwnerPlatformRole is auth.RequirePermission alone: the resource is
	// platform-wide and has no tenant to bind to.
	OwnerPlatformRole Owner = "auth.RequirePermission"
	// OwnerTokenSubject is handler.AuthHandler.tokenForCaller: the token named
	// by {tokenHash} must belong to the caller.
	OwnerTokenSubject Owner = "handler.AuthHandler.tokenForCaller"
	// OwnerRuntimeSecret is the per-project derived runtime secret.
	OwnerRuntimeSecret Owner = "per-project runtime secret"
	// OwnerFunctionAudience is the JWT audience claim naming the project.
	OwnerFunctionAudience Owner = "JWT aud names the project"
	// OwnerBucketVisibility is the bucket's own public flag.
	OwnerBucketVisibility Owner = "bucket public flag"
)

// Path parameters that name a tenant.
const (
	ParamNone    = ""
	ParamProject = "projectId"
	ParamOrg     = "orgId"
)

// Row is one route's authorization contract. Reads and writes on the same
// pattern are separate rows whenever their requirements differ.
type Row struct {
	// Methods are the HTTP methods this contract covers. MethodAll expands to
	// every method chi registers for a catch-all mount.
	Methods []string
	// Pattern is the chi route pattern exactly as chi.Walk reports it.
	Pattern string
	Auth    AuthClass
	// Permission is the platform permission the mount requires, or "".
	Permission string
	// Param is the path parameter naming the tenant this route acts on.
	Param string
	Owner Owner
	// MinRole is the minimum org role, or "" when the route needs none
	// beyond whatever Owner resolves.
	MinRole string
	// Unrestricted marks a route that refuses any narrowed PAT — a
	// scope-limited or project-bound credential is not enough.
	Unrestricted bool
	// Capability is the capability a platform service token may call this
	// route with, or "" when no capability token may reach it.
	Capability string
	// ServiceOnly marks a route that ONLY a capability token may call, so a
	// studio session or an ordinary PAT is refused.
	ServiceOnly bool
	// Note records the non-obvious reason for this row's contract.
	Note string
}

// Table returns the complete route authorization table.
func Table() []Row {
	rows := make([]Row, 0, 128)
	rows = append(rows, publicRows...)
	rows = append(rows, platformRows...)
	rows = append(rows, orgRows...)
	rows = append(rows, vaultRows...)
	rows = append(rows, provisionRows...)
	rows = append(rows, schemaRows...)
	rows = append(rows, projectRows...)
	rows = append(rows, appRows...)
	rows = append(rows, runtimeRows...)
	return rows
}
