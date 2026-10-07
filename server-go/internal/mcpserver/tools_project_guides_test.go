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
	for _, want := range []string{"numeric", "string claim", `"42"`} {
		if !strings.Contains(idType, want) {
			t.Errorf("userIdType lacks %q: %s", want, idType)
		}
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
