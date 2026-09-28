package routepolicy

// provisionRows cover /api/provision and its per-project subtree. Every row
// below {projectId} is bound by RequireProjectAccess, so a caller the project
// is not visible to gets 404 before any role is considered.
var provisionRows = []Row{
	{Methods: get, Pattern: "/api/provision/", Auth: AuthSession, Owner: OwnerNone, Note: "lists only the caller's own projects"},
	{Methods: post, Pattern: "/api/provision/", Auth: AuthSession, Owner: OwnerNone, Note: "tier admission decides whether the caller's org may provision"},
	{Methods: post, Pattern: "/api/provision/estimate", Auth: AuthSession, Owner: OwnerNone},

	{Methods: get, Pattern: "/api/provision/{projectId}/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer},
	{Methods: get, Pattern: "/api/provision/{projectId}/logs", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Database: true},
	{Methods: get, Pattern: "/api/provision/{projectId}/maintenance-window", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Database: true},
	{Methods: put, Pattern: "/api/provision/{projectId}/maintenance-window", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},

	{Methods: del, Pattern: "/api/provision/{projectId}/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Note: "stops the project and hard-deletes it after the grace period"},
	{Methods: post, Pattern: "/api/provision/{projectId}/deletion/cancel", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleOwner},
	{Methods: get, Pattern: "/api/provision/{projectId}/credentials", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Note: "hands out the tenant database's superuser password", Database: true},
	{Methods: post, Pattern: "/api/provision/{projectId}/credentials/rotate", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Database: true},
	{Methods: get, Pattern: "/api/provision/{projectId}/documentdb/users/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Note: "names and roles only; a password is shown once, at create or rotate", Database: true},
	{Methods: post, Pattern: "/api/provision/{projectId}/documentdb/users/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Note: "creates a Mongo-only login in the project's cluster", Database: true},
	{Methods: post, Pattern: "/api/provision/{projectId}/documentdb/users/{username}/rotate", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Database: true},
	{Methods: del, Pattern: "/api/provision/{projectId}/documentdb/users/{username}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Database: true},
	{Methods: patch, Pattern: "/api/provision/{projectId}/deletion-protection", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleOwner, Note: "turning it off is what makes a project deletable"},
	{Methods: post, Pattern: "/api/provision/{projectId}/backups/purge", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin},
	{Methods: post, Pattern: "/api/provision/{projectId}/pause", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Database: true},
	{Methods: post, Pattern: "/api/provision/{projectId}/resume", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Database: true},
	{Methods: post, Pattern: "/api/provision/{projectId}/upgrade", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Note: "restarts the tenant database onto the newest patch of its own major", Database: true},
	{Methods: get, Pattern: "/api/provision/{projectId}/cluster", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Note: "the database's disk, size, plan and tunable settings; no secrets", Database: true},
	{Methods: post, Pattern: "/api/provision/{projectId}/storage", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Note: "grows the tenant database's disk, up to its plan", Database: true},
	{Methods: post, Pattern: "/api/provision/{projectId}/tier", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Note: "moves the tenant database onto its org's plan; only a platform admin changes the plan", Database: true},
	{Methods: put, Pattern: "/api/provision/{projectId}/parameters", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Note: "allowlisted Postgres settings within the plan's bounds", Database: true},

	{Methods: post, Pattern: "/api/provision/{projectId}/database", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Note: "creates the database of a project made without one, as project creation does"},

	{Methods: get, Pattern: "/api/provision/{projectId}/metrics/current", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Database: true},
	{Methods: get, Pattern: "/api/provision/{projectId}/metrics/history", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Database: true},
	{Methods: get, Pattern: "/api/provision/{projectId}/performance/summary", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Database: true},
	{Methods: get, Pattern: "/api/provision/{projectId}/performance/top-queries", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Database: true},
	{Methods: get, Pattern: "/api/provision/{projectId}/performance/wait-events", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Database: true},

	{Methods: get, Pattern: "/api/provision/{projectId}/audit/config", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},
	{Methods: get, Pattern: "/api/provision/{projectId}/audit/logs", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},
	{Methods: post, Pattern: "/api/provision/{projectId}/audit/enable", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},
	{Methods: get, Pattern: "/api/provision/{projectId}/migrations/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},
	{Methods: post, Pattern: "/api/provision/{projectId}/migrations/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},

	{Methods: get, Pattern: "/api/provision/{projectId}/rls-policies/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Capability: "policies:read", Database: true},
	{Methods: get, Pattern: "/api/provision/{projectId}/rls-policies/{policyId}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Capability: "policies:read", Database: true},
	{Methods: post, Pattern: "/api/provision/{projectId}/rls-policies/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},
	{Methods: patch, Pattern: "/api/provision/{projectId}/rls-policies/{policyId}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},
	{Methods: del, Pattern: "/api/provision/{projectId}/rls-policies/{policyId}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},
	{Methods: get, Pattern: "/api/provision/{projectId}/column-policies/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Capability: "policies:read", Database: true},
	{Methods: get, Pattern: "/api/provision/{projectId}/column-policies/{policyId}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Capability: "policies:read", Database: true},
	{Methods: post, Pattern: "/api/provision/{projectId}/column-policies/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},
	{Methods: patch, Pattern: "/api/provision/{projectId}/column-policies/{policyId}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},
	{Methods: del, Pattern: "/api/provision/{projectId}/column-policies/{policyId}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},
	{Methods: get, Pattern: "/api/provision/{projectId}/table-grants/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Capability: "policies:read", Note: "the engine fetches grants in the same round as the two policy sets", Database: true},
	{Methods: post, Pattern: "/api/provision/{projectId}/table-grants/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},
	{Methods: patch, Pattern: "/api/provision/{projectId}/table-grants/{grantId}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},
	{Methods: del, Pattern: "/api/provision/{projectId}/table-grants/{grantId}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},

	{Methods: post, Pattern: "/api/provision/{projectId}/backup/trigger", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Database: true},
	{Methods: get, Pattern: "/api/provision/{projectId}/backup/list", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Database: true},
	{Methods: post, Pattern: "/api/provision/{projectId}/backup/restore", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Database: true},
	{Methods: get, Pattern: "/api/provision/{projectId}/backup/restore/{jobId}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Note: "the job is read as (projectId, jobId)", Database: true},
	{Methods: get, Pattern: "/api/provision/{projectId}/backup/wal-lag", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Database: true},
	{Methods: post, Pattern: "/api/provision/{projectId}/backup/schedule", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Database: true},
	{Methods: del, Pattern: "/api/provision/{projectId}/backup/schedule", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Database: true},

	{Methods: post, Pattern: "/api/provision/{projectId}/snapshot/export", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Note: "a snapshot is a whole-database dump", Database: true},
	{Methods: get, Pattern: "/api/provision/{projectId}/snapshot/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Database: true},
	{Methods: get, Pattern: "/api/provision/{projectId}/snapshot/{snapshotId}/download", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Note: "the snapshot is read as (projectId, snapshotId)", Database: true},
	{Methods: del, Pattern: "/api/provision/{projectId}/snapshot/{snapshotId}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Database: true},
}

// projectRows cover the /api/projects/{projectId}/* data-plane subtrees.
// Studio's DocumentDB document browser.
const (
	databasesRoute   = "/api/projects/{projectId}/documentdb/databases"
	collectionsRoute = databasesRoute + "/{database}/collections"
	collectionRoute  = collectionsRoute + "/{collection}"
	documentsRoute   = collectionRoute + "/documents"
	indexesRoute     = collectionRoute + "/indexes"
)

var projectRows = []Row{
	{Methods: get, Pattern: "/api/projects/{projectId}/info/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Capability: "projects:info:read"},
	{Methods: get, Pattern: "/api/alerts/project/{projectId}/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer},

	{Methods: get, Pattern: "/api/projects/{projectId}/functions/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer},
	{Methods: get, Pattern: "/api/projects/{projectId}/functions/_metadata", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer},
	{Methods: get, Pattern: "/api/projects/{projectId}/functions/runtime/status", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer},
	{Methods: get, Pattern: "/api/projects/{projectId}/functions/{fnId}/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Note: "the function is read as (projectId, fnId)"},
	{Methods: get, Pattern: "/api/projects/{projectId}/functions/{fnId}/logs", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer},
	{Methods: post, Pattern: "/api/projects/{projectId}/functions/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper},
	{Methods: del, Pattern: "/api/projects/{projectId}/functions/{fnId}/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper},
	{Methods: post, Pattern: "/api/projects/{projectId}/functions/{fnId}/invoke", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "the studio's test-run path; end users invoke via /functions/v1"},
	{Methods: get, Pattern: "/api/projects/{projectId}/functions/secrets", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "function secrets hold third-party API keys"},
	{Methods: post, Pattern: "/api/projects/{projectId}/functions/secrets", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper},
	{Methods: del, Pattern: "/api/projects/{projectId}/functions/secrets/{key}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper},
	{Methods: get, Pattern: "/api/projects/{projectId}/functions/egress", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper},
	{Methods: put, Pattern: "/api/projects/{projectId}/functions/egress", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "changes what deployed code may reach"},

	{Methods: post, Pattern: "/api/projects/{projectId}/schema/apply", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},
	{Methods: get, Pattern: "/api/projects/{projectId}/cors/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "the allowlist decides which web apps may call the data plane"},
	{Methods: put, Pattern: "/api/projects/{projectId}/cors/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper},
	{Methods: get, Pattern: "/api/projects/{projectId}/db-endpoint/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "reports the host, port and cluster CA a client needs; never the password", Database: true},
	{Methods: put, Pattern: "/api/projects/{projectId}/db-endpoint/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Note: "opening the database to the internet is an admin decision", Database: true},
	{Methods: get, Pattern: "/api/projects/{projectId}/sdk-keys/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, Note: "prefixes and dates only; the key itself is shown once, at creation"},
	{Methods: post, Pattern: "/api/projects/{projectId}/sdk-keys/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "relayed to auth with the control plane's own short key-admin token"},
	{Methods: del, Pattern: "/api/projects/{projectId}/sdk-keys/{keyId}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper},
	{Methods: get, Pattern: "/api/projects/{projectId}/auth-settings/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper},
	{Methods: put, Pattern: "/api/projects/{projectId}/auth-settings/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper},

	{Methods: get, Pattern: "/api/projects/{projectId}/realtime/tables", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Database: true},
	{Methods: put, Pattern: "/api/projects/{projectId}/realtime/tables/{schema}/{table}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "alters the database publication", Database: true},
	{Methods: del, Pattern: "/api/projects/{projectId}/realtime/tables/{schema}/{table}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},
	{Methods: post, Pattern: "/api/projects/{projectId}/realtime/enable-all", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},
	{Methods: post, Pattern: "/api/projects/{projectId}/realtime/disable-all", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},

	{Methods: get, Pattern: databasesRoute, Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Database: true},
	{Methods: get, Pattern: collectionsRoute, Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Database: true},
	{Methods: post, Pattern: collectionsRoute, Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},
	{Methods: del, Pattern: collectionRoute + "/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "drops the collection and every document in it", Database: true},
	{Methods: get, Pattern: documentsRoute, Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Database: true},
	{Methods: post, Pattern: documentsRoute, Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},
	{Methods: put, Pattern: documentsRoute, Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},
	{Methods: patch, Pattern: documentsRoute, Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},
	{Methods: del, Pattern: documentsRoute, Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},
	{Methods: get, Pattern: collectionRoute + "/count", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Database: true},
	{Methods: get, Pattern: collectionRoute + "/sample", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Database: true},
	{Methods: get, Pattern: indexesRoute, Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Database: true},
	{Methods: post, Pattern: indexesRoute, Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},
	{Methods: del, Pattern: collectionRoute + "/indexes/{index}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Database: true},

	{Methods: get, Pattern: "/api/projects/{projectId}/storage/buckets", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer},
	{Methods: get, Pattern: "/api/projects/{projectId}/storage/buckets/{bucket}/objects", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer},
	{Methods: get, Pattern: "/api/projects/{projectId}/storage/buckets/{bucket}/objects/*", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer},
	{Methods: get, Pattern: "/api/projects/{projectId}/storage/buckets/{bucket}/download-url/*", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer},
	{Methods: post, Pattern: "/api/projects/{projectId}/storage/buckets", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper},
	{Methods: del, Pattern: "/api/projects/{projectId}/storage/buckets/{bucket}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "destroys the bucket and every object in it — the same rung as DROP TABLE"},
	{Methods: post, Pattern: "/api/projects/{projectId}/storage/buckets/{bucket}/upload-url", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper},
	{Methods: post, Pattern: "/api/projects/{projectId}/storage/buckets/{bucket}/confirm-upload", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper},
	{Methods: del, Pattern: "/api/projects/{projectId}/storage/buckets/{bucket}/objects/*", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper},

	// tus mounts every method on two patterns. HEAD reports an upload's
	// offset (a read); everything else creates, appends to or terminates an
	// upload, so it lands on the authoring rung with the rest of storage.
	{Methods: tusReads, Pattern: "/api/projects/{projectId}/storage/tus", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer},
	{Methods: tusWrites, Pattern: "/api/projects/{projectId}/storage/tus", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper},
	{Methods: tusReads, Pattern: "/api/projects/{projectId}/storage/tus/*", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer},
	{Methods: tusWrites, Pattern: "/api/projects/{projectId}/storage/tus/*", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper},
}

// runtimeRows are the surfaces no studio credential reaches: end-user function
// invocation, the Deno runtime's callbacks, the public object fast-path and
// the service-only mail relay.
var runtimeRows = []Row{
	{Methods: get, Pattern: "/storage/v1/object/public/{projectId}/{bucket}/*", Auth: AuthPublic, Param: ParamProject, Owner: OwnerBucketVisibility, Note: "serves only buckets the tenant marked public"},
	{Methods: anyVerb, Pattern: "/functions/v1/{projectId}/http/*", Auth: AuthFunctionJWT, Param: ParamProject, Owner: OwnerFunctionAudience},
	{Methods: anyVerb, Pattern: "/functions/v1/{projectId}/{fnId}", Auth: AuthFunctionJWT, Param: ParamProject, Owner: OwnerFunctionAudience},

	{Methods: post, Pattern: "/internal/runtime/functions/{fnId}/metadata", Auth: AuthRuntimeToken, Owner: OwnerRuntimeSecret, Note: "the project is named in the BODY, not the path, and the token must match that project's derived secret"},
	{Methods: post, Pattern: "/internal/invoke/{projectId}/{fnId}", Auth: AuthRuntimeToken, Param: ParamProject, Owner: OwnerRuntimeSecret},
	{Methods: post, Pattern: "/internal/storage/{projectId}/upload-url", Auth: AuthRuntimeToken, Param: ParamProject, Owner: OwnerRuntimeSecret},
	{Methods: post, Pattern: "/internal/storage/{projectId}/confirm-upload", Auth: AuthRuntimeToken, Param: ParamProject, Owner: OwnerRuntimeSecret},
	{Methods: post, Pattern: "/internal/storage/{projectId}/download-url", Auth: AuthRuntimeToken, Param: ParamProject, Owner: OwnerRuntimeSecret},
	{Methods: get, Pattern: "/internal/storage/{projectId}/metadata/{storageId}", Auth: AuthRuntimeToken, Param: ParamProject, Owner: OwnerRuntimeSecret},
	{Methods: del, Pattern: "/internal/storage/{projectId}/{storageId}", Auth: AuthRuntimeToken, Param: ParamProject, Owner: OwnerRuntimeSecret},

	{Methods: post, Pattern: "/internal/email/send", Auth: AuthSession, Owner: OwnerNone, Capability: "email:send", ServiceOnly: true, Note: "the auth service has no mail SDK of its own; a studio session is refused"},
}
