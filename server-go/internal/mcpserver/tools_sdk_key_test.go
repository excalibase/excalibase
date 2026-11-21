package mcpserver

import (
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/sdkkeys"
	"github.com/excalibase/provisioning-poc/internal/service"
)

func listedKeys() string {
	return `{"keys":[` + realJSON(sdkkeys.Key{ID: 7, KeyPrefix: "esk_pub_ab12", KeyType: "publishable", Name: "web", CreatedAt: "2026-11-01T00:00:00Z"}, nil) + "," +
		realJSON(sdkkeys.Key{ID: 8, KeyPrefix: "esk_sec_cd34", KeyType: "secret", Name: "server", CreatedAt: "2026-11-01T00:00:00Z"}, nil) + `]}`
}

func sdkKeyRouteCases() []routeCase {
	return []routeCase{
		{
			name: "revoke_publishable_key", tool: "revoke_publishable_key", args: map[string]any{"project_id": testProjectA, "key_id": 7},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/sdk-keys/", 200, listedKeys())
				f.on(http.MethodDelete, projectsA+"/sdk-keys/7", 204, ``)
			},
			expect: []string{"GET " + projectsA + "/sdk-keys/", "DELETE " + projectsA + "/sdk-keys/7"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				if out["revoked"] != float64(7) || out["keyPrefix"] != "esk_pub_ab12" {
					t.Errorf("out = %v", out)
				}
			},
		},
		{
			name: "list_templates", tool: "list_templates", args: map[string]any{"project_id": testProjectA},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/app-templates/", 200, "["+realJSON(service.TemplateView{
					ID: "valkey", Format: "excalibase", Name: "Valkey", Summary: "A Redis-compatible store", Category: "database", License: "BSD-3-Clause", Catalog: true,
					Apps: []service.TemplateAppView{{Name: "valkey", Image: "valkey/valkey:8", Internal: true, InternalPorts: []int{6379}, Replicas: 1,
						Env: []service.TemplateVarView{{Name: "VALKEY_PASSWORD", Source: "generated", ShowOnce: true}}}},
					NeedsPrivateNetwork: true, Fit: service.TemplateFit{Plan: "free", AppsNeeded: 1, AppsAllowed: 3, AppCPU: "500m", AppMemory: "512Mi", Refusals: []string{}},
					Source: "name: valkey\nsecret: do-not-show",
				}, plantedFields)+"]")
			},
			expect: []string{"GET " + projectsA + "/app-templates/"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				templates, _ := out["templates"].([]any)
				if len(templates) != 1 {
					t.Fatalf("out = %v", out)
				}
				template, _ := templates[0].(map[string]any)
				fit, _ := template["fit"].(map[string]any)
				if template["id"] != "valkey" || template["needsPrivateNetwork"] != true || fit["appsAllowed"] != float64(3) {
					t.Errorf("template = %v", template)
				}
				text := realJSON(out, nil)
				for _, hidden := range []string{"plantedSecret", "do-not-show", "VALKEY_PASSWORD", "source"} {
					if strings.Contains(text, hidden) {
						t.Errorf("the answer carries %q", hidden)
					}
				}
			},
		},
	}
}

// Only a publishable key is revoked here: a secret key is the user's to remove in Studio.
func TestRevokePublishableKeyRefusesASecretKey(t *testing.T) {
	f := newFakeRoutes()
	f.on(http.MethodGet, projectsA+"/sdk-keys/", 200, listedKeys())
	res := callTool(t, session(t, f, &recordingAudit{}, writeCaller()), "revoke_publishable_key", map[string]any{"project_id": testProjectA, "key_id": 8})
	if !res.IsError || !strings.Contains(resultText(res), "publishable") {
		t.Fatalf("result = %s", resultText(res))
	}
	for _, call := range f.calls {
		if call.Method == http.MethodDelete {
			t.Fatal("a secret key was revoked")
		}
	}
}

func TestRevokePublishableKeyRefusesAKeyThatIsNotListed(t *testing.T) {
	f := newFakeRoutes()
	f.on(http.MethodGet, projectsA+"/sdk-keys/", 200, listedKeys())
	cs := session(t, f, &recordingAudit{}, writeCaller())
	for _, id := range []int{99, 0, -1} {
		if res := callTool(t, cs, "revoke_publishable_key", map[string]any{"project_id": testProjectA, "key_id": id}); !res.IsError {
			t.Errorf("key %d accepted", id)
		}
	}
	for _, call := range f.calls {
		if call.Method == http.MethodDelete {
			t.Fatal("an unlisted key was revoked")
		}
	}
}
