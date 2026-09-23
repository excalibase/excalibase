package routepolicy

// appRows cover /api/projects/{projectId}/apps — the customer applications a
// project runs beside its database (EXC-378).
//
// An app is data-plane authoring: it declares what the project serves, and it
// is undone by authoring it back. So it sits on the developer rung with edge
// functions and storage buckets, reads on the viewer rung, and deleting one
// stays with the developer — a bucket and a function are destroyed on that
// rung too, and project lifecycle (pause, resume, teardown) is what admin
// covers. The deploy lifecycle is not here at all: it is EXC-386's, and its
// rows come with it.
var appRows = []Row{
	{Methods: get, Pattern: "/api/projects/{projectId}/apps/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer},
	{Methods: get, Pattern: "/api/projects/{projectId}/apps/{appId}/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Note: "the app is read as (projectId, appId)"},
	{Methods: post, Pattern: "/api/projects/{projectId}/apps/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper},
	{Methods: patch, Pattern: "/api/projects/{projectId}/apps/{appId}/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "changes the image, env, port and replicas the next deploy would run"},
	{Methods: del, Pattern: "/api/projects/{projectId}/apps/{appId}/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper},

	{Methods: post, Pattern: "/api/projects/{projectId}/apps/{appId}/deploy", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper},
	{Methods: get, Pattern: "/api/projects/{projectId}/apps/{appId}/deploys", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer},
	{Methods: post, Pattern: "/api/projects/{projectId}/apps/{appId}/deploys/{deployId}/redeploy", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "rolls an earlier deploy's frozen config out again as a new deploy; never edits the app"},
}
