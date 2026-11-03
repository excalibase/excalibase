package mcpserver

import (
	"net/http"
	"strings"
	"testing"
)

func projectInfo(t *testing.T) map[string]any {
	t.Helper()
	routes := newFakeRoutes()
	routes.on(http.MethodGet, projectsA+"/info/", 200, `{"projectId":"proj-a","projectName":"A","corsAllowedOrigins":[]}`)
	routes.on(http.MethodGet, schemaA+"/tables", 200, `[]`)
	routes.on(http.MethodGet, projectsA+"/sdk-keys/", 200, `{"keys":[]}`)
	cs := session(t, routes, &recordingAudit{}, readOnlyCaller())
	return structured(t, callTool(t, cs, "get_project_info", map[string]any{"project_id": testProjectA}))
}

func TestProjectInfoStatesTheEndUserIdTypeAndHowToCompareIt(t *testing.T) {
	out := projectInfo(t)
	auth, _ := out["endUserAuth"].(map[string]any)
	permissions, _ := out["permissions"].(map[string]any)
	idType, _ := auth["userIdType"].(string)
	// EXC-641: auth signs userId as a JSON number and carries the email in sub only.
	for _, want := range []string{"JSON number", `"userId": 42`, "no email claim", "sub", "X-Excalibase-Email"} {
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
