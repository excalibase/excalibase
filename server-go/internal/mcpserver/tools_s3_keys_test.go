package mcpserver

import (
	"slices"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
)

// s3On is the settings with the S3 flag on, for tests that call the tools.
func s3On() Settings {
	settings := testSettings
	settings.Features = features.NewStatic(features.S3)
	return settings
}

func TestRevokeS3KeyRefusesAnIDThatIsNoKey(t *testing.T) {
	routes := newFakeRoutes()
	cs := sessionWith(t, routes, &recordingAudit{}, writeCaller(), s3On())
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
	cs := sessionWith(t, routes, &recordingAudit{}, readOnlyCaller(), s3On())
	res := callTool(t, cs, "list_s3_keys", map[string]any{"project_id": testProjectA})
	if res.IsError || strings.Contains(resultText(res), "leaked") || !strings.Contains(resultText(res), "EXS3AAAAAAAAAAAAAAAA") {
		t.Fatalf("list = %s", resultText(res))
	}
}

func TestS3KeyToolsAreNotListedWhileTheFlagIsOff(t *testing.T) {
	listed := listedTools(t, testSettings)
	for _, name := range []string{"list_s3_keys", "revoke_s3_key"} {
		if slices.Contains(listed, name) {
			t.Errorf("%s is announced with FEATURE_S3 off", name)
		}
	}
}
