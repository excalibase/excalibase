package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/sdkkeys"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

// EXC-684: every write tool added for the gap-epic features meets the same
// role gate as its route, and no tool reaches another org's project.
func exc684WriteToolCalls() map[string]map[string]any {
	app := map[string]any{"project_id": matrixProjectA, "app_id": "web"}
	with := func(extra map[string]any) map[string]any {
		merged := map[string]any{}
		for key, value := range app {
			merged[key] = value
		}
		for key, value := range extra {
			merged[key] = value
		}
		return merged
	}
	return map[string]map[string]any{
		"set_app_secret":    with(map[string]any{"name": "API_KEY", "value": "not-a-real-value"}),
		"redeploy_app":      with(map[string]any{"deploy_id": "d1"}),
		"add_app_domain":    with(map[string]any{"hostname": "www.example.com"}),
		"verify_app_domain": with(map[string]any{"domain_id": "dom1"}),
		"remove_app_domain": with(map[string]any{"domain_id": "dom1"}),
		// Every optional field is given, so the tool does not read the current connection (a viewer may) before it writes.
		"connect_app_repo":       with(map[string]any{"repository": "owner/name", "root_dir": ".", "dockerfile_path": ".", "build_args": map[string]string{}}),
		"disconnect_app_repo":    with(nil),
		"invoke_function":        {"project_id": matrixProjectA, "function_id": "x"},
		"delete_function_secret": {"project_id": matrixProjectA, "key": "API_KEY"},
		"create_storage_bucket":  {"project_id": matrixProjectA, "name": "avatars"},
		"set_bucket_access":      {"project_id": matrixProjectA, "bucket": "avatars", "access": map[string]any{"user": map[string]any{"read": "own"}}},
		"revoke_publishable_key": {"project_id": matrixProjectA, "key_id": 7},
		"rebuild_app":            with(nil),
	}
}

// appReads stands in for the app store the harness leaves offline: only the read the tools make first.
type appReads struct {
	apphost.Store
}

func (appReads) Get(projectID, id string) (*apphost.App, error) {
	return &apphost.App{ID: id, ProjectID: projectID, Name: id, Image: "nginx:1"}, nil
}

// keyManager stands in for excalibase-auth's key admin and records what is revoked.
type keyManager struct{ revoked []int64 }

func (*keyManager) List(context.Context, string, string) ([]sdkkeys.Key, error) {
	return []sdkkeys.Key{
		{ID: 7, KeyPrefix: "esk_pub_ab12", KeyType: "publishable", Name: "web"},
		{ID: 8, KeyPrefix: "esk_sec_cd34", KeyType: "secret", Name: "server"},
	}, nil
}

func (*keyManager) Create(context.Context, string, string, sdkkeys.CreateRequest) (*sdkkeys.CreatedKey, error) {
	return nil, errors.New("not used")
}

func (k *keyManager) Revoke(_ context.Context, _, _ string, id int64) error {
	k.revoked = append(k.revoked, id)
	return nil
}

func exc684Server(t *testing.T) (*httptest.Server, *keyManager) {
	t.Helper()
	keys := &keyManager{}
	server, _ := mcpServerTweaked(t, features.NewStatic(features.All()...), func(deps *handlerDeps, instances *fakestore.Instances, orgs *fakestore.Orgs) {
		orgs.AddOrg(matrixOrgA, domain.Free)
		deps.appHandler = handler.NewAppHandler(appReads{}, handler.NewProjectSourceLookup(instances, service.NewOrgPlanTiers(instances, orgs)), apphost.Route{})
		deps.sdkKeysHandler = handler.NewSDKKeysHandler(keys, instances, orgs, nil)
	})
	return server, keys
}

func TestEXC684WriteToolsRefuseAViewer(t *testing.T) {
	server, keys := exc684Server(t)
	viewer, err := mcpConnect(t, server.URL+"/mcp", mcpViewPAT)
	if err != nil {
		t.Fatalf("connect viewer: %v", err)
	}
	for name, args := range exc684WriteToolCalls() {
		res := mcpCall(t, viewer, name, args)
		if !res.IsError || !strings.Contains(mcpText(res), "insufficient project role") {
			t.Errorf("%s as a viewer = %s", name, mcpText(res))
		}
	}
	if len(keys.revoked) != 0 {
		t.Errorf("a viewer revoked keys %v", keys.revoked)
	}
}

// Tools that only read are still refused when their route is Developer's: secret names and build logs.
func TestEXC684DeveloperOnlyReadsRefuseAViewer(t *testing.T) {
	server, _ := exc684Server(t)
	viewer, err := mcpConnect(t, server.URL+"/mcp", mcpViewPAT)
	if err != nil {
		t.Fatalf("connect viewer: %v", err)
	}
	for name, args := range map[string]map[string]any{
		"list_function_secrets": {"project_id": matrixProjectA},
		"list_app_builds":       {"project_id": matrixProjectA, "app_id": "web", "build_id": "b1"},
	} {
		res := mcpCall(t, viewer, name, args)
		if !res.IsError || !strings.Contains(mcpText(res), "insufficient project role") {
			t.Errorf("%s as a viewer = %s", name, mcpText(res))
		}
	}
}

// A developer revokes a publishable key and only that; a secret key is left for Studio.
func TestEXC684DeveloperRevokesOnlyAPublishableKey(t *testing.T) {
	server, keys := exc684Server(t)
	dev, err := mcpConnect(t, server.URL+"/mcp", mcpDevPAT)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if res := mcpCall(t, dev, "revoke_publishable_key", map[string]any{"project_id": matrixProjectA, "key_id": 8}); !res.IsError {
		t.Fatalf("a secret key was revoked: %s", mcpText(res))
	}
	if res := mcpCall(t, dev, "revoke_publishable_key", map[string]any{"project_id": matrixProjectA, "key_id": 7}); res.IsError {
		t.Fatalf("a developer could not revoke a publishable key: %s", mcpText(res))
	}
	if len(keys.revoked) != 1 || keys.revoked[0] != 7 {
		t.Errorf("revoked = %v, want [7]", keys.revoked)
	}
}

func TestEXC684ToolsDoNotReachAnotherOrgsProject(t *testing.T) {
	server, _ := mcpServer(t)
	dev, err := mcpConnect(t, server.URL+"/mcp", mcpDevPAT)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	for name, args := range exc684WriteToolCalls() {
		args["project_id"] = matrixProjectB
		res := mcpCall(t, dev, name, args)
		text := mcpText(res)
		if !res.IsError || !(strings.Contains(text, "404") || strings.Contains(text, "not found")) {
			t.Errorf("%s against another org's project = %s", name, text)
		}
		if strings.Contains(text, "site") {
			t.Errorf("%s answers an app tool in site words: %s", name, text)
		}
	}
}

// Read tools reach the same project routes, so another org's project reads as absent too.
func TestEXC684ReadToolsDoNotReachAnotherOrgsProject(t *testing.T) {
	server, _ := mcpServer(t)
	dev, err := mcpConnect(t, server.URL+"/mcp", mcpDevPAT)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	for name, args := range map[string]map[string]any{
		"get_metrics":           {"project_id": matrixProjectB, "container_range": "1h"},
		"list_function_secrets": {"project_id": matrixProjectB},
		"list_storage_buckets":  {"project_id": matrixProjectB},
		"list_storage_objects":  {"project_id": matrixProjectB, "bucket": "avatars"},
		"list_templates":        {"project_id": matrixProjectB},
		"list_app_domains":      {"project_id": matrixProjectB, "app_id": "web"},
	} {
		res := mcpCall(t, dev, name, args)
		text := mcpText(res)
		if !res.IsError || !(strings.Contains(text, "404") || strings.Contains(text, "not found")) {
			t.Errorf("%s against another org's project = %s", name, text)
		}
	}
}
