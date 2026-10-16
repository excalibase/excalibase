package routepolicy

const sitePath = "/api/projects/{projectId}/sites/{siteId}/"

// siteRows cover /api/projects/{projectId}/sites (EXC-353). A site is what the
// project's frontend serves, authored like an app: reads on the viewer rung,
// every change on the developer rung.
var siteRows = []Row{
	{Methods: get, Pattern: "/api/projects/{projectId}/sites/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Discloses: NoSecret, Note: "answers 503 past the gates on an install without a Cloudflare account"},
	{Methods: post, Pattern: "/api/projects/{projectId}/sites/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "claims a hostname under the user-content domain"},
	{Methods: get, Pattern: sitePath, Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Discloses: NoSecret, Note: "the site is read as (projectId, siteId)"},
	{Methods: patch, Pattern: sitePath, Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "changes the routing the next deploy is published with"},
	{Methods: del, Pattern: sitePath, Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "deletes the site's Worker, then the CORS origin the site added, then its history"},
	{Methods: post, Pattern: "/api/projects/{projectId}/sites/{siteId}/deploy", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "the body is the build output as tar or tar.gz, checked before anything is sent to Cloudflare"},
	{Methods: get, Pattern: "/api/projects/{projectId}/sites/{siteId}/deploys", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Discloses: NoSecret},
	{Methods: get, Pattern: "/api/projects/{projectId}/sites/{siteId}/deploys/{deployId}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Discloses: NoSecret, Note: "one deploy, read as (projectId, siteId, deployId)"},
	{Methods: post, Pattern: "/api/projects/{projectId}/sites/{siteId}/deploys/{deployId}/rollback", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "publishes an earlier deploy's files again and makes it live"},
}
