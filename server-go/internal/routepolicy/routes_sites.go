package routepolicy

const sitePath = "/api/projects/{projectId}/sites/{siteId}/"

// siteRows cover /api/projects/{projectId}/sites (EXC-353). A site is what the
// project's frontend serves, authored like an app: reads on the viewer rung,
// every change on the developer rung. A site's deploy token (EXC-620) reaches
// only the deploy upload and the deploy history of its own site.
var siteRows = []Row{
	{Methods: get, Pattern: "/api/projects/{projectId}/sites/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Discloses: NoSecret, Note: "answers 503 past the gates on an install without a Cloudflare account"},
	{Methods: post, Pattern: "/api/projects/{projectId}/sites/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "claims a hostname under the user-content domain"},
	{Methods: get, Pattern: sitePath, Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Discloses: NoSecret, Note: "the site is read as (projectId, siteId)"},
	{Methods: patch, Pattern: sitePath, Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "changes the routing the next deploy is published with"},
	{Methods: del, Pattern: sitePath, Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "deletes the site's Worker, then the CORS origin the site added, then its history"},
	{Methods: post, Pattern: "/api/projects/{projectId}/sites/{siteId}/deploy", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, SiteDeployToken: true, Note: "the body is the build output as tar or tar.gz, checked before anything is sent to Cloudflare; refused past the plan's deploys per hour"},
	{Methods: get, Pattern: "/api/projects/{projectId}/sites/{siteId}/deploys", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Discloses: NoSecret, SiteDeployToken: true},
	{Methods: get, Pattern: "/api/projects/{projectId}/sites/{siteId}/deploys/{deployId}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Discloses: NoSecret, SiteDeployToken: true, Note: "one deploy, read as (projectId, siteId, deployId)"},
	{Methods: get, Pattern: "/api/projects/{projectId}/sites/limits", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Discloses: NoSecret, Note: "the plan's site count, deploy size and deploy rate"},
	{Methods: get, Pattern: "/api/projects/{projectId}/sites/{siteId}/tokens", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Discloses: NoSecret, Note: "names and prefixes only; the token itself is shown once, on create"},
	{Methods: post, Pattern: "/api/projects/{projectId}/sites/{siteId}/tokens", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "mints a deploy token scoped to this site; kept only as its SHA-256"},
	{Methods: del, Pattern: "/api/projects/{projectId}/sites/{siteId}/tokens/{tokenId}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "revocation is immediate: every request looks the token up"},
	{Methods: post, Pattern: "/api/projects/{projectId}/sites/{siteId}/deploys/{deployId}/rollback", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "publishes an earlier deploy's files again and makes it live"},
}
