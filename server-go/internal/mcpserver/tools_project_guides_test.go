package mcpserver

import (
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
)

func projectInfo(t *testing.T) map[string]any { return projectInfoWith(t) }

func projectInfoWith(t *testing.T, on ...features.Feature) map[string]any {
	t.Helper()
	settings := testSettings
	settings.Features = features.NewStatic(on...)
	routes := newFakeRoutes()
	routes.on(http.MethodGet, projectsA+"/info/", 200, `{"projectId":"proj-a","projectName":"A","corsAllowedOrigins":[]}`)
	routes.on(http.MethodGet, schemaA+"/tables", 200, `[]`)
	routes.on(http.MethodGet, projectsA+"/sdk-keys/", 200, `{"keys":[]}`)
	cs := sessionWith(t, routes, &recordingAudit{}, readOnlyCaller(), settings)
	return structured(t, callTool(t, cs, "get_project_info", map[string]any{"project_id": testProjectA}))
}

func TestProjectInfoStatesTheEndUserIdTypeAndHowToCompareIt(t *testing.T) {
	out := projectInfo(t)
	auth, _ := out["endUserAuth"].(map[string]any)
	permissions, _ := out["permissions"].(map[string]any)
	idType, _ := auth["userIdType"].(string)
	// EXC-641: auth signs userId as a JSON number; with guests off the sub is the address.
	for _, want := range []string{"JSON number", `"userId": 42`, "email claim", "sub", "X-Excalibase-Email"} {
		if !strings.Contains(idType, want) {
			t.Errorf("userIdType lacks %q: %s", want, idType)
		}
	}
	if strings.Contains(idType, "never a JSON number") {
		t.Errorf("userIdType still says the claim is a string: %s", idType)
	}
	compare, _ := permissions["ownerColumnType"].(string)
	for _, want := range []string{"bigint", "text", "uuid", "invalid_session_variable"} {
		if !strings.Contains(compare, want) {
			t.Errorf("ownerColumnType lacks %q: %s", want, compare)
		}
	}
}

func TestProjectInfoSaysCorsChangesTakeAboutAMinute(t *testing.T) {
	out := projectInfo(t)
	cors, _ := out["cors"].(map[string]any)
	if note, _ := cors["note"].(string); !strings.Contains(note, "1 minute") {
		t.Fatalf("note = %q", note)
	}
}

// A customer backend verifies end-user tokens offline against the public keys.
func TestProjectInfoNamesThePublicKeysABackendVerifiesWith(t *testing.T) {
	out := projectInfo(t)
	auth, _ := out["endUserAuth"].(map[string]any)
	urls, _ := out["endpoints"].(map[string]any)
	verify, _ := auth["verifyOffline"].(string)
	for _, want := range []string{urls["auth"].(string) + "/.well-known/jwks.json", "ES256", "aud", "exp"} {
		if !strings.Contains(verify, want) {
			t.Errorf("verifyOffline lacks %q: %s", want, verify)
		}
	}
}

func guide(t *testing.T, out map[string]any, key string) map[string]any {
	t.Helper()
	section, _ := out[key].(map[string]any)
	if section == nil {
		t.Fatalf("%s missing: %v", key, out)
	}
	return section
}

func mustContain(t *testing.T, label, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("%s lacks %q: %s", label, want, text)
		}
	}
}

// With every sign-in flag off the guide is the password and key guide only.
func TestAuthGuideLeavesOutMethodsWhoseFlagIsOff(t *testing.T) {
	auth := guide(t, projectInfo(t), "endUserAuth")
	for _, key := range []string{"oauth", "emailCode", "guest", "account"} {
		if _, ok := auth[key]; ok {
			t.Errorf("%s is in the guide while its flag is off", key)
		}
	}
	idType, _ := auth["userIdType"].(string)
	if strings.Contains(idType, "account id") {
		t.Errorf("userIdType speaks of guests while authguest is off: %s", idType)
	}
}

func TestAuthGuideDescribesProviderSignInWhileAuthOAuthIsOn(t *testing.T) {
	out := projectInfoWith(t, features.AuthOAuth)
	auth := guide(t, out, "endUserAuth")
	urls, _ := out["endpoints"].(map[string]any)
	text, _ := auth["oauth"].(string)
	mustContain(t, "oauth", text, "GET "+urls["auth"].(string)+"/authorize", "provider=google", "github", "redirect_to", "code_challenge", "S256",
		`"grant_type":"pkce"`, `"auth_code"`, `"code_verifier"`, "method_not_enabled", "get_auth_settings")
	for _, key := range []string{"emailCode", "guest", "account"} {
		if _, ok := auth[key]; ok {
			t.Errorf("%s is in the guide while only authoauth is on", key)
		}
	}
}

func TestAuthGuideDescribesEmailLinkAndCodeWhileAuthOTPIsOn(t *testing.T) {
	out := projectInfoWith(t, features.AuthOTP)
	auth := guide(t, out, "endUserAuth")
	urls, _ := out["endpoints"].(map[string]any)
	text, _ := auth["emailCode"].(string)
	mustContain(t, "emailCode", text, "POST "+urls["auth"].(string)+"/otp", `"type":"magiclink"`, `"type":"email"`, "create_user", "code_challenge",
		"POST "+urls["auth"].(string)+"/verify", "token_hash", `"token"`, "code_verifier", "method_not_enabled")
	if _, ok := auth["oauth"]; ok {
		t.Error("oauth is in the guide while authoauth is off")
	}
}

func TestAuthGuideDescribesGuestSignInAndTheStableSubjectWhileAuthGuestIsOn(t *testing.T) {
	out := projectInfoWith(t, features.AuthGuest)
	auth := guide(t, out, "endUserAuth")
	permissions := guide(t, out, "permissions")
	urls, _ := out["endpoints"].(map[string]any)
	guest, _ := auth["guest"].(string)
	mustContain(t, "guest", guest, "POST "+urls["auth"].(string)+"/guest", "captchaToken", "captcha_required", "isGuest", "role guest", "method_not_enabled")
	idType, _ := auth["userIdType"].(string)
	mustContain(t, "userIdType", idType, "JSON number", "sub", "account id", "email claim", "is_guest")
	if strings.Contains(idType, "the token's sub is the user's email") {
		t.Errorf("userIdType still says sub is the email: %s", idType)
	}
	variables, _ := permissions["sessionVariables"].(string)
	mustContain(t, "sessionVariables", variables, "X-Excalibase-Email", "email claim", "X-Excalibase-Is-Guest")
	if strings.Contains(variables, "(the sub claim)") {
		t.Errorf("sessionVariables still reads the email from sub: %s", variables)
	}
	if strings.Contains(strings.ToLower(guest+idType+variables), "anonymous") {
		t.Error("guest accounts are never called anonymous")
	}
}

func TestAuthGuideDescribesAccountSelfServiceWhileAuthUserIsOn(t *testing.T) {
	out := projectInfoWith(t, features.AuthUser)
	auth := guide(t, out, "endUserAuth")
	urls, _ := out["endpoints"].(map[string]any)
	text, _ := auth["account"].(string)
	mustContain(t, "account", text, "GET "+urls["auth"].(string)+"/user", "PATCH", "scope=global", "scope=others")
}

// sessionVariables read the same with guests off: the email claim, never sub.
func TestSessionVariablesNameTheEmailClaimWithGuestsOff(t *testing.T) {
	variables, _ := guide(t, projectInfo(t), "permissions")["sessionVariables"].(string)
	mustContain(t, "sessionVariables", variables, "X-Excalibase-Email", "email claim")
	if strings.Contains(variables, "Is-Guest") {
		t.Errorf("Is-Guest named while authguest is off: %s", variables)
	}
}
