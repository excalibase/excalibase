package main

import (
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/excalibase/provisioning-poc/internal/trust"
)

const appIDArg = "app_id"

// EXC-899: the MCP tools that create, deploy, build or scale an app reach the
// router in-process, so a limited owner is refused there with the same words.
func TestMCPContainerToolsRefuseALimitedOwner(t *testing.T) {
	server, _ := mcpServerTweaked(t, features.NewStatic(features.All()...), func(deps *handlerDeps, instances *fakestore.Instances, orgs *fakestore.Orgs) {
		orgs.AddOrg(matrixOrgA, domain.Free)
		deps.appHandler = handler.NewAppHandler(appReads{}, handler.NewProjectSourceLookup(instances, service.NewOrgPlanTiers(instances, orgs)), apphost.Route{})
		deps.trustGate = trust.NewGate(stubTrust{trust.Limited}, trust.Policy{MinAgeDays: 90, MinPublicEvents: 5, ActivityWindowDays: 90, MinRepos: 3, RepoMinAgeDays: 30})
	})
	dev, err := mcpConnect(t, server.URL+"/mcp", mcpDevPAT)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	with := func(extra map[string]any) map[string]any {
		merged := map[string]any{"project_id": matrixProjectA}
		for key, value := range extra {
			merged[key] = value
		}
		return merged
	}
	calls := map[string]map[string]any{
		"create_app":       with(map[string]any{"name": "web", "image": "ghcr.io/acme/web:1"}),
		"deploy_app":       with(map[string]any{appIDArg: "web"}),
		"redeploy_app":     with(map[string]any{appIDArg: "web", "deploy_id": "d1"}),
		"rebuild_app":      with(map[string]any{appIDArg: "web"}),
		"update_app":       with(map[string]any{appIDArg: "web", "replicas": 3}),
		"connect_app_repo": with(map[string]any{appIDArg: "web", "repository": "owner/name", "root_dir": ".", "dockerfile_path": ".", "build_args": map[string]string{}}),
		"deploy_function":  with(map[string]any{"id": "hello", "files": []map[string]any{{"path": "index.ts", "content": "export default () => new Response('ok')"}}}),
		"rebuild_site":     with(map[string]any{"site_id": "s1"}),
	}
	for name, args := range calls {
		res := mcpCallConfirmed(t, dev, name, args)
		if !res.IsError || !strings.Contains(mcpText(res), "verified account") {
			t.Errorf("%s by a limited owner = error:%v %s, want the verification refusal", name, res.IsError, mcpText(res))
		}
	}
}
