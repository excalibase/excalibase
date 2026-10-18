package mcpserver

import (
	"strings"
	"testing"
)

func TestRevokeS3KeyRefusesAnIDThatIsNoKey(t *testing.T) {
	routes := newFakeRoutes()
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	for _, id := range []string{"", "../buckets", "s3k_1/../../x", "EXS3AAAAAAAAAAAAAAAA"} {
		if res := callTool(t, cs, "revoke_s3_key", map[string]any{"project_id": testProjectA, "key_id": id}); !res.IsError {
			t.Errorf("%q was not refused", id)
		}
	}
	if len(routes.calls) != 0 {
		t.Fatalf("a refused id reached the API: %+v", routes.calls)
	}
}

func TestListS3KeysNeverCarriesASecret(t *testing.T) {
	routes := newFakeRoutes()
	routes.on("GET", projectsA+"/storage/s3-keys", 200,
		`{"keys":[{"id":"s3k_1","accessKeyId":"EXS3AAAAAAAAAAAAAAAA","secretAccessKey":"leaked"}],"endpoint":"https://s3.example.test","region":"us-east-1"}`)
	cs := session(t, routes, &recordingAudit{}, readOnlyCaller())
	res := callTool(t, cs, "list_s3_keys", map[string]any{"project_id": testProjectA})
	if res.IsError || strings.Contains(resultText(res), "leaked") || !strings.Contains(resultText(res), "EXS3AAAAAAAAAAAAAAAA") {
		t.Fatalf("list = %s", resultText(res))
	}
}
