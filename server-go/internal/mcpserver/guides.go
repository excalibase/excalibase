package mcpserver

import "strings"

// authGuide is how a page's end users sign up and sign in (excalibase-auth).
// They are the project's own users, never Studio accounts.
func authGuide(urls map[string]string, on signInFlags) map[string]any {
	auth := urls["auth"]
	guide := map[string]any{
		"who": "End users of the app, stored in the project's own database; not Studio accounts.",
		"register": "POST " + auth + `/register {"email","password","fullName"} (all required) → 201 {"accessToken","refreshToken","tokenType":"Bearer","expiresIn",` +
			`"user":{"id","email","fullName"}}; when the project requires email verification it answers 201 {"emailVerificationRequired":true,"message","user"} and no tokens. 409 when the email is taken.`,
		"signIn":  "POST " + auth + `/token {"grant_type":"password","email","password"} → the same token answer as register.`,
		"anon":    "POST " + auth + `/token {"grant_type":"api_key","api_key":"<publishable key>"} → {"accessToken"} for the anon role, no refresh token.`,
		"refresh": "POST " + auth + `/token {"grant_type":"refresh_token","refresh_token":"<refreshToken>"} → a new access and refresh token; refresh tokens rotate, so keep only the newest (one tab refreshes at a time).`,
		"logout":  "POST " + auth + `/logout {"refreshToken":"<refreshToken>"} revokes that session.`,
		"claims": "The access token is a JWT. userId is the end user's id and role is the role it runs as (\"user\" after sign-up, unless an admin changed it; " +
			"\"anon\" for the publishable-key exchange). Send it as Authorization: Bearer <accessToken> to REST, GraphQL, realtime and functions.",
		"userIdType": userIDTypeText(on.guest),
		"verifyOffline": "A backend verifies an access token without calling auth: fetch the public keys (JWKS) from " + auth + "/.well-known/jwks.json, " +
			"check the ES256 signature and exp, and that aud includes exactly this project's audience (excalibase:<projectId> by default; the aud of a token test_auth_flow shows): " +
			"the key signs every project's tokens, so the aud check is what keeps another project's users out. Cache the keys for a few minutes and fetch them again when a signature does not verify.",
		"cors": "The browser calls these from the page's origin, so that origin must be on the project's CORS list (list_cors_origins, add_cors_origin); " +
			"test_api_request with origin checks the sign-in call too.",
		"settings": "Email verification and the site URL in verification and password-reset links are project sign-in settings; MCP does not change them. " +
			"The site URL (absolute https, or http://localhost) is required: without it register (when verification is on), resend-verification and forgot-password answer 422 site_url_required and send nothing. " +
			"Set it in Studio → Authentication → Settings.",
	}
	on.addTo(guide, auth)
	return guide
}

// signInFlags are the end-user sign-in features switched on for the platform.
// A project must also turn each method on; get_auth_settings shows which are.
type signInFlags struct{ oauth, otp, guest, user bool }

func (on signInFlags) addTo(guide map[string]any, auth string) {
	if on.oauth {
		guide["oauth"] = "Sign in with Google or GitHub when the project turned the provider on (get_auth_settings lists them): send the browser to " +
			"GET " + auth + "/authorize?provider=google|github&redirect_to=<page>&code_challenge=<S256 challenge>&code_challenge_method=S256. " +
			"redirect_to must be the project's site URL or one of its redirect URLs, and the challenge is the S256 hash of a random code_verifier the page keeps. " +
			"The browser returns to redirect_to with ?code=<code> (or ?error=<code> such as access_denied or signup_disabled). The code works once, for 5 minutes: " +
			`POST ` + auth + `/token {"grant_type":"pkce","auth_code":"<code>","code_verifier":"<verifier>"} → the same token answer as sign-in. ` +
			"A provider the project has not turned on answers 404 method_not_enabled."
	}
	if on.otp {
		guide["emailCode"] = "Sign in with an emailed link or code when the project turned it on. " +
			`POST ` + auth + `/otp {"email","type":"magiclink"|"email","code_challenge":"<S256 challenge>","code_challenge_method":"S256","redirect_to":"<page>","create_user":true|false} ` +
			`(type defaults to magiclink; redirect_to is for a link and defaults to the site URL; create_user defaults to true) → 200 {"message"} for every address, so it never shows whether an account exists. ` +
			`The link lands on redirect_to with ?token_hash=<hash>&type=magiclink: POST ` + auth + `/verify {"type":"magiclink","token_hash":"<hash>","code_verifier":"<verifier>"}. ` +
			`An 8-digit code is spent with POST ` + auth + `/verify {"type":"email","email","token":"<code>","code_verifier":"<verifier>"}. ` +
			"Either answers with the token answer; both last 10 minutes, a code allows 5 guesses, and sends are paced per address and client (429 rate_limited). " +
			"When the project has not turned it on, both answer 404 method_not_enabled."
	}
	if on.guest {
		guide["guest"] = "A visitor with no address can use the app as a guest when the project turned guests on. " +
			`POST ` + auth + `/guest with {} (or {"captchaToken":"<Turnstile token>"} when the project requires a captcha; without it 400 captcha_required) → the token answer with ` +
			`"user":{"id","isGuest":true} and no address. The account has role guest, which has no grants until a permission names guest. ` +
			"The session refreshes like any other. Guest sign-ups are paced (429 rate_limited), unused guests are deleted after a period of inactivity (30 days by default), and a project that has not turned guests on answers 404 method_not_enabled. " +
			"A guest who links an address or provider (POST " + auth + "/user/identities/email or /authorize, while that method is on) becomes a full account."
	}
	if on.user {
		guide["account"] = "A signed-in end user manages their own account with their access token. GET " + auth + "/user answers the account with its metadata; " +
			`PATCH ` + auth + `/user {"fullName","data","email","password","currentPassword","emailRedirectTo"} changes it (a new password needs currentPassword; a new address is confirmed by mail first). ` +
			"POST " + auth + `/logout?scope=global ends every session of the user and ?scope=others every one but this one (body {"refreshToken"} as for a plain logout). ` +
			"While the project has this off these answer 404 method_not_enabled."
	}
}

// userIDTypeText says how the token names the user. Guests make the sub the
// account id; without them the sub is the address.
func userIDTypeText(guests bool) string {
	text := "userId is the user's database id as a JSON number (\"userId\": 42); read it as a number in function code. "
	if guests {
		return text + "The token's sub is the account id as a string (\"42\"), never the address, so a guest has one too. " +
			"The address is the email claim (permissions read it as X-Excalibase-Email); a guest has no email claim and an is_guest claim of true, and an engine version that falls back to the sub would read the account id as the guest's X-Excalibase-Email, so identify a guest by X-Excalibase-Is-Guest or X-Excalibase-User-Id. " +
			"test_auth_flow shows the real claims of a test sign-in. See permissions.ownerColumnType for comparing userId with an owner column."
	}
	return text + "The token's sub is the user's email. X-Excalibase-Email is read from the email claim when the token has one; an engine version that falls back to the sub reads that otherwise, " +
		"and one that does not has no X-Excalibase-Email for a token with no email claim, so a permission that uses it fails with missing_session_variable. " +
		"test_auth_flow shows the real claims of a test sign-in, so check it before relying on the email claim. See permissions.ownerColumnType for comparing userId with an owner column."
}

// permissionsGuide is how a permission names the signed-in user.
func permissionsGuide(guests bool) map[string]any {
	variables := "A permission compares columns with session variables taken from the caller's JWT: X-Excalibase-User-Id is the userId claim, " +
		"X-Excalibase-Role the role, X-Excalibase-Email the email claim (when the token has none, an engine version that falls back to the sub reads that, and a newer one leaves it absent)"
	if guests {
		variables += ", X-Excalibase-Is-Guest the is_guest claim"
	}
	variables += ", and any other claim <name> is X-Excalibase-<name>. Names ignore case."
	return map[string]any{
		"sessionVariables": variables,
		"rowFilter":        `select/update/delete filter: {"owner_id": {"_eq": "X-Excalibase-User-Id"}} — only the caller's rows.`,
		"insertPreset": `insert {"check": {}, "set": {"owner_id": "X-Excalibase-User-Id"}, "columns": ["title"]}: set fills owner_id from the session; ` +
			`leave it out of columns so the client cannot send it. insert always needs a check ({} allows any row).`,
		"updateCheck": `update check: {"owner_id": {"_eq": "X-Excalibase-User-Id"}} keeps an update from moving a row to someone else.`,
		"ownerColumnType": "The session variable X-Excalibase-User-Id is always a string (\"42\"), whatever the claim's JSON type; the engine converts it to the owner column's type before comparing. " +
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
