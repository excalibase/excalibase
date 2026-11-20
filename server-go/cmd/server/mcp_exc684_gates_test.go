package main

import (
	"strings"
	"testing"
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
		"connect_app_repo":    with(map[string]any{"repository": "owner/name", "root_dir": ".", "dockerfile_path": ".", "build_args": map[string]string{}}),
		"disconnect_app_repo": with(nil),
		"rebuild_app":         with(nil),
	}
}

func TestEXC684WriteToolsRefuseAViewer(t *testing.T) {
	server, _ := mcpServer(t)
	viewer, err := mcpConnect(t, server.URL+"/mcp", mcpViewPAT)
	if err != nil {
		t.Fatalf("connect viewer: %v", err)
	}
	for name, args := range exc684WriteToolCalls() {
		if name == "set_app_secret" {
			// It reads the app first, which this harness's offline database cannot answer; the PUT's Developer gate is in the authz matrix.
			continue
		}
		res := mcpCall(t, viewer, name, args)
		if !res.IsError || !strings.Contains(mcpText(res), "insufficient project role") {
			t.Errorf("%s as a viewer = %s", name, mcpText(res))
		}
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
