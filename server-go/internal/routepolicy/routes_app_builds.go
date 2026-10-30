package routepolicy

const appBuildPath = "/api/projects/{projectId}/apps/{appId}/"

// appBuildRows cover git builds of apps (EXC-547), on the same rungs as a site's.
// Every row but the disconnect answers 404 while the appgitbuilds flag is off.
var appBuildRows = []Row{
	{Methods: get, Pattern: appBuildPath + "git", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Discloses: NoSecret, Note: "the connected repository and how its image is built; build arguments are declared non-secret"},
	{Methods: put, Pattern: appBuildPath + "git", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "the caller's own GitHub sign-in must show the repository; the service refuses a caller below the rung before anything is saved"},
	{Methods: del, Pattern: appBuildPath + "git", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "answers with the flag off too, so a connection can always be removed"},
	{Methods: post, Pattern: appBuildPath + "git/sign-in", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "binds a single-use state to the caller, project and app"},
	{Methods: get, Pattern: appBuildPath + "git/repositories", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Discloses: NoSecret, Note: "only what the caller's own sign-in showed them"},
	{Methods: get, Pattern: appBuildPath + "builds", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Discloses: NoSecret},
	{Methods: post, Pattern: appBuildPath + "builds", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "counts toward the plan's app builds per hour"},
	{Methods: get, Pattern: appBuildPath + "builds/{buildId}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Discloses: NoSecret, Note: "the build log, developers only; build-visible secret values are redacted"},
}
