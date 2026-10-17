package routepolicy

const siteBuildPath = "/api/projects/{projectId}/sites/{siteId}/"

// siteBuildRows cover git builds of sites (EXC-354). Reads on the viewer rung,
// changes and anything touching a sign-in or secret names on the developer rung.
// Every row answers 404 while SITES_GIT_BUILDS_ENABLED is off.
var siteBuildRows = []Row{
	{Methods: get, Pattern: "/api/projects/{projectId}/sites/build-secrets", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Discloses: NoSecret, Note: "secret names and whether builds see them; values never leave the vault"},
	{Methods: put, Pattern: "/api/projects/{projectId}/sites/build-secrets", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "a build-visible secret ends up in the built files; Studio warns"},
	{Methods: get, Pattern: siteBuildPath + "git", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Discloses: NoSecret, Note: "the connected repository and build settings; build variables are declared non-secret"},
	{Methods: put, Pattern: siteBuildPath + "git", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "the caller's own GitHub sign-in must show the repository"},
	{Methods: del, Pattern: siteBuildPath + "git", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper},
	{Methods: post, Pattern: siteBuildPath + "git/sign-in", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "binds a single-use state to the caller, project and site"},
	{Methods: get, Pattern: siteBuildPath + "git/repositories", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Discloses: NoSecret, Note: "only what the caller's own sign-in showed them"},
	{Methods: get, Pattern: siteBuildPath + "builds", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Discloses: NoSecret},
	{Methods: post, Pattern: siteBuildPath + "builds", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "counts toward the project's deploys per hour"},
	{Methods: get, Pattern: siteBuildPath + "builds/{buildId}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Discloses: NoSecret, Note: "the build log, developers only: build output can print what a viewer should not see; build-visible secret values are redacted"},
	{Methods: post, Pattern: "/api/sites/github/webhook", Auth: AuthPublic, Note: "X-Hub-Signature-256 over the raw body is the credential; 5 MiB cap before it is checked"},
	{Methods: get, Pattern: "/api/sites/github/callback", Auth: AuthPublic, Discloses: NoSecret, Note: "the single-use, hashed, ten-minute state is the credential"},
}
