package routepolicy

// appRows cover /api/projects/{projectId}/apps. An app is data-plane
// authoring, like a function or a bucket: reads on the viewer rung, every
// write, including pause, resume and deletion of the app, on the developer rung.
var appRows = []Row{
	{Methods: get, Pattern: "/api/projects/{projectId}/apps/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer},
	{Methods: get, Pattern: "/api/projects/{projectId}/apps/{appId}/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Note: "the app is read as (projectId, appId)"},
	{Methods: post, Pattern: "/api/projects/{projectId}/apps/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper},
	{Methods: patch, Pattern: "/api/projects/{projectId}/apps/{appId}/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "changes the image, env, port and replicas the next deploy would run"},
	{Methods: del, Pattern: "/api/projects/{projectId}/apps/{appId}/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "tears the workload down and waits for its pods to be gone before the app is forgotten; an app with a disk needs confirmDeleteDisk"},
	{Methods: post, Pattern: "/api/projects/{projectId}/apps/{appId}/pause", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "scales the app to zero; its config and deploy history stay"},
	{Methods: post, Pattern: "/api/projects/{projectId}/apps/{appId}/resume", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper},
	{Methods: post, Pattern: "/api/projects/{projectId}/apps/{appId}/disk", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "grows the app's disk up to its plan's cap, or lowers a stopped app's disk to no less than it holds; the same rung that attaches one"},
	{Methods: get, Pattern: "/api/projects/{projectId}/apps/{appId}/disk", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "what the disk holds against its size and the plan's cap; it takes the app's lease and may start a short probe, so the rung that deploys"},

	{Methods: post, Pattern: "/api/projects/{projectId}/apps/{appId}/deploy", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper},
	{Methods: get, Pattern: "/api/projects/{projectId}/apps/{appId}/logs", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Note: "reads only the pods labelled with this app's id in this project's namespace; viewer, like the project's and a function's logs"},
	{Methods: get, Pattern: "/api/projects/{projectId}/apps/{appId}/deploys", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer},
	{Methods: post, Pattern: "/api/projects/{projectId}/apps/{appId}/deploys/{deployId}/redeploy", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "rolls an earlier deploy's frozen config out again as a new deploy; never edits the app"},
	{Methods: put, Pattern: "/api/projects/{projectId}/apps/{appId}/secrets/{name}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "write-only: stores a secret variable's value in the project vault and points the variable at it; no route returns the value"},

	{Methods: get, Pattern: "/api/projects/{projectId}/app-network/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Note: "whether the project's apps may reach each other; mounted only with the Postgres platform store"},
	{Methods: put, Pattern: "/api/projects/{projectId}/app-network/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleAdmin, Note: "opening app-to-app traffic in the project is an admin decision"},

	{Methods: get, Pattern: "/api/projects/{projectId}/app-templates/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Note: "the built-in templates and what each costs this project's plan; no secret value exists before a deploy"},
	{Methods: get, Pattern: "/api/projects/{projectId}/app-templates/{templateId}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Note: "one template with its source document"},
	{Methods: post, Pattern: "/api/projects/{projectId}/app-templates/{templateId}/deploy", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "creates and deploys the template's apps all or nothing; a template that turns on the private network also needs admin, checked in the handler as PUT app-network is"},

	{Methods: get, Pattern: "/api/projects/{projectId}/registry-credentials/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Note: "names the registries that have a credential; never a username or a password"},
	{Methods: put, Pattern: "/api/projects/{projectId}/registry-credentials/{registry}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "write-only: stores the credential in the project vault; no route returns it"},
	{Methods: del, Pattern: "/api/projects/{projectId}/registry-credentials/{registry}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "also deletes the pull secrets rendered from it"},

	{Methods: get, Pattern: "/api/projects/{projectId}/apps/{appId}/certificate", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Note: "whether the app's own hostname is served over HTTPS yet; mounted only when APP_DOMAIN_ISSUER is set"},
	{Methods: get, Pattern: "/api/projects/{projectId}/apps/{appId}/domains/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleViewer, Note: "mounted only when APP_DOMAIN_ISSUER is set"},
	{Methods: post, Pattern: "/api/projects/{projectId}/apps/{appId}/domains/", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "claims nothing: a domain is routed only once verified"},
	{Methods: post, Pattern: "/api/projects/{projectId}/apps/{appId}/domains/{domainId}/verify", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper, Note: "routes the domain only if its CNAME names this app's own hostname"},
	{Methods: del, Pattern: "/api/projects/{projectId}/apps/{appId}/domains/{domainId}", Auth: AuthSession, Param: ParamProject, Owner: OwnerProjectAccess, MinRole: roleDeveloper},
}
