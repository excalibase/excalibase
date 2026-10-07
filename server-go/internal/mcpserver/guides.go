package mcpserver

import "strings"

// authGuide is how a page's end users sign up and sign in (excalibase-auth).
// They are the project's own users, never Studio accounts.
func authGuide(urls map[string]string) map[string]any {
	auth := urls["auth"]
	return map[string]any{
		"who": "End users of the app, stored in the project's own database; not Studio accounts.",
		"register": "POST " + auth + `/register {"email","password","fullName"} (all required) → 201 {"accessToken","refreshToken","tokenType":"Bearer","expiresIn",` +
			`"user":{"id","email","fullName"}}; when the project requires email verification it answers 201 {"emailVerificationRequired":true,"message","user"} and no tokens. 409 when the email is taken.`,
		"signIn":  "POST " + auth + `/token {"grant_type":"password","email","password"} → the same token answer as register.`,
		"anon":    "POST " + auth + `/token {"grant_type":"api_key","api_key":"<publishable key>"} → {"accessToken"} for the anon role, no refresh token.`,
		"refresh": "POST " + auth + `/token {"grant_type":"refresh_token","refresh_token":"<refreshToken>"} → a new access and refresh token; refresh tokens rotate, so keep only the newest (one tab refreshes at a time).`,
		"logout":  "POST " + auth + `/logout {"refreshToken":"<refreshToken>"} revokes that session.`,
		"claims": "The access token is a JWT. userId is the end user's id and role is the role it runs as (\"user\" after sign-up, unless an admin changed it; " +
			"\"anon\" for the publishable-key exchange). Send it as Authorization: Bearer <accessToken> to REST, GraphQL, realtime and functions.",
		"userIdType": "userId is numeric (the user's database id, e.g. 42) but travels as a string claim (\"42\"), never a JSON number: " +
			"test_auth_flow shows the real claims of a test sign-in. See permissions.ownerColumnType for comparing it with an owner column.",
		"cors": "The browser calls these from the page's origin, so that origin must be on the project's CORS list (list_cors_origins, add_cors_origin); " +
			"test_api_request with origin checks the sign-in call too.",
		"settings": "Email verification and the site URL in verification and password-reset links are project sign-in settings; MCP does not change them. " +
			"The site URL (absolute https, or http://localhost) is required: without it register (when verification is on), resend-verification and forgot-password answer 422 site_url_required and send nothing. " +
			"Set it in Studio → Authentication → Settings.",
	}
}

// permissionsGuide is how a permission names the signed-in user.
func permissionsGuide() map[string]any {
	return map[string]any{
		"sessionVariables": "A permission compares columns with session variables taken from the caller's JWT: X-Excalibase-User-Id is the userId claim, " +
			"X-Excalibase-Role the role, X-Excalibase-Email the email, and any other claim <name> is X-Excalibase-<name>. Names ignore case.",
		"rowFilter": `select/update/delete filter: {"owner_id": {"_eq": "X-Excalibase-User-Id"}} — only the caller's rows.`,
		"insertPreset": `insert {"check": {}, "set": {"owner_id": "X-Excalibase-User-Id"}, "columns": ["title"]}: set fills owner_id from the session; ` +
			`leave it out of columns so the client cannot send it. insert always needs a check ({} allows any row).`,
		"updateCheck": `update check: {"owner_id": {"_eq": "X-Excalibase-User-Id"}} keeps an update from moving a row to someone else.`,
		"ownerColumnType": "X-Excalibase-User-Id is always a string (\"42\"); the engine converts it to the owner column's type before comparing. " +
			"A bigint or integer owner column compares as a number and a text or varchar one as text, so the same {\"_eq\": \"X-Excalibase-User-Id\"} works for both. " +
			"A uuid column cannot hold \"42\": the request fails with invalid_session_variable. Make the owner column bigint (matching the user id) or text, and never uuid.",
		"anon": "anon has no userId: a permission that uses X-Excalibase-User-Id fails for anon, so give anon its own (or no) permission.",
		"keys": anonByDesign,
	}
}

// realtimeGuide is the subscription protocol the engine speaks.
func realtimeGuide(graphqlEndpoint string) map[string]any {
	socket := strings.Replace(graphqlEndpoint, "http", "ws", 1)
	return map[string]any{
		"url":     socket + " with the WebSocket subprotocol graphql-transport-ws",
		"enable":  "A table publishes changes only after set_realtime enabled: true; a subscription to any other table never fires.",
		"connect": `send {"type":"connection_init","payload":{"Authorization":"Bearer <accessToken>"}} and wait for {"type":"connection_ack"}`,
		"subscribe": `{"id":"1","type":"subscribe","payload":{"query":"subscription { publicTodosChanges { operation table data timestamp } }"}} ` +
			"— the field is the table's graphql.subscription name (get_graphql_schema).",
		"events": `{"type":"next","id":"1","payload":{"data":{"publicTodosChanges":{"operation":"INSERT|UPDATE|DELETE","table":...,"data":{row},"timestamp":...}}}}; ` +
			"stop with {\"id\":\"1\",\"type\":\"complete\"}.",
		"permissions": "Events pass through the subscriber's select permission: a socket hears only rows (and columns) its role may read.",
	}
}

// studioOnly is what MCP never does, and where in Studio a person does it.
func studioOnly(studioURL, projectID string) map[string]string {
	base := strings.TrimRight(studioURL, "/")
	project := base + "/project/" + projectID
	return map[string]string{
		"personalAccessTokens": "Studio → Access tokens: " + base + "/account/tokens",
		"secretKeys":           "Studio → API keys: " + project + "/api-keys (only publishable keys are made through MCP)",
		"functionSecrets":      "Studio → Edge Functions → Secrets: " + project + "/edge-functions (keeps the value out of the conversation)",
		"endUsers":             "Studio → Auth Users: " + project + "/auth/users",
		"signInSettings":       "Sign-in providers and email verification are set by the project's admins, never through MCP.",
		"projectLifecycle":     "Creating or deleting projects, members, plans, backups and credentials: Studio → Settings: " + project + "/settings",
	}
}
