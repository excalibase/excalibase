package mcpserver

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
)

// fieldCase is one answer carrying every field the descriptions promise.
type fieldCase struct {
	tool     string
	features []features.Feature
	setup    func(f *fakeRoutes)
	// present are dotted paths that must reach the agent.
	present []string
}

func lookup(value any, path string) bool {
	for _, key := range splitPath(path) {
		object, ok := value.(map[string]any)
		if !ok {
			return false
		}
		if value, ok = object[key]; !ok {
			return false
		}
	}
	return true
}

func splitPath(path string) []string {
	var parts []string
	start := 0
	for i := 0; i <= len(path); i++ {
		if i == len(path) || path[i] == '.' {
			parts = append(parts, path[start:i])
			start = i + 1
		}
	}
	return parts
}

// The views keep the fields the tool descriptions promise.
func TestViewsKeepTheFieldsTheDescriptionsPromise(t *testing.T) {
	cases := []fieldCase{
		{
			tool: "get_database_info",
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, provisionA+"/cluster", 200, `{"tier":"FREE","orgTier":"FREE","storageSize":"2Gi","storageStart":"1Gi","storageLimit":"5Gi","storageUsedBytes":5,"instances":1,"cpu":"1","memory":"1Gi","parameters":{"a":"b"},"tunableParameters":["a"]}`)
			},
			present: []string{"storageLimit", "storageUsedBytes", "cpu", "memory", "tunableParameters", "parameters", "tier"},
		},
		{
			tool: "get_db_endpoint",
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/db-endpoint/", 200, `{"host":"h","port":1,"requireTls":true,"database":"d","username":"u","caCertificate":"CA",`+
					`"connectionStrings":{"requireTls":"a","allowPlaintext":"b","mongoRequireTls":"m"},"internal":{"host":"i","port":1,"connectionString":"c"},"mongoPort":2,"mongoAvailable":true}`)
			},
			present: []string{"caCertificate", "internal.host", "internal.connectionString", "connectionStrings.requireTls", "connectionStrings.mongoRequireTls", "mongoPort", "mongoAvailable"},
		},
		{
			tool: "get_db_read_address", features: []features.Feature{features.DBReadAddress},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/db-read-address/", 200, `{"replicas":2,"publicOpen":true,"enabled":true,"available":true,"host":"h","port":1,"connectionString":"c","username":"ro","readOnlyLogin":true,"internal":{"host":"i","port":1,"connectionString":"c"}}`)
			},
			present: []string{"publicOpen", "connectionString", "readOnlyLogin", "internal.connectionString", "replicas"},
		},
		{
			tool: "get_auth_settings", features: []features.Feature{features.AuthOAuth},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/auth-settings/", 200, `{"requireEmailVerification":true,"siteUrl":"u","redirectUrls":["r"],"allowNewUsers":true,"emailOtpEnabled":true,"guestSignInEnabled":true,"guestCaptchaSecretSet":true,"mfaEnabled":true}`)
				f.on(http.MethodGet, projectsA+"/auth-providers/", 200, `{"providers":[{"provider":"github","enabled":true,"clientId":"id","clientSecretSet":true,"callbackUrl":"cb"}]}`)
			},
			present: []string{"emailOtpEnabled", "guestCaptchaSecretSet", "mfaEnabled", "redirectUrls", "allowNewUsers", "guestSignInEnabled", "providers"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			routes := newFakeRoutes()
			tc.setup(routes)
			settings := testSettings
			settings.Features = features.NewStatic(tc.features...)
			cs := sessionWith(t, routes, &recordingAudit{}, writeCaller(), settings)
			out := structured(t, callTool(t, cs, tc.tool, map[string]any{"project_id": testProjectA}))
			for _, path := range tc.present {
				if !lookup(out, path) {
					raw, _ := json.Marshal(out)
					t.Errorf("%s dropped %s: %s", tc.tool, path, raw)
				}
			}
		})
	}
}

// A provider keeps the fields the description promises.
func TestAuthProvidersKeepTheirFields(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodGet, projectsA+"/auth-settings/", 200, `{"requireEmailVerification":true,"siteUrl":"u"}`)
	routes.on(http.MethodGet, projectsA+"/auth-providers/", 200, `{"providers":[{"provider":"github","enabled":true,"clientId":"id","clientSecretSet":true,"callbackUrl":"cb"}]}`)
	settings := testSettings
	settings.Features = features.NewStatic(features.AuthOAuth)
	cs := sessionWith(t, routes, &recordingAudit{}, writeCaller(), settings)
	out := structured(t, callTool(t, cs, "get_auth_settings", map[string]any{"project_id": testProjectA}))
	providers, _ := out["providers"].([]any)
	provider, _ := providers[0].(map[string]any)
	for _, key := range []string{"clientId", "clientSecretSet", "callbackUrl", "provider", "enabled"} {
		if _, ok := provider[key]; !ok {
			t.Errorf("provider lost %s: %v", key, provider)
		}
	}
}
