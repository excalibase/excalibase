package mcpserver

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// offeredTools is the whole catalogue. A tool added or removed changes this
// list on purpose: the endpoint is built for coding, and every addition is a
// decision about what an AI tool may do with a person's credential.
var offeredTools = []string{
	"add_cors_origin", "apply_migration", "create_app", "create_publishable_key", "delete_function", "deploy_app",
	"deploy_function", "describe_table", "execute_sql", "generate_typescript_types", "get_ci_snippet",
	"get_deploy_status", "get_dockerfile_template", "get_graphql_schema", "get_logs", "get_project_info",
	"list_apps", "list_cors_origins", "list_functions", "list_migrations", "list_permissions", "list_projects",
	"list_tables", "remove_cors_origin", "set_db_function_permission", "set_function_outbound_hosts",
	"set_function_secret", "set_permission", "set_realtime", "test_api_request", "track_db_function",
}

// allowedRoutes is every route a tool may call. Anything else is refused by
// TestNoToolLeavesItsAllowedRoutes, so a new call is a visible decision.
// test_api_request also calls the project's own data API, as anon (a
// publishable key) or as one of its signed-in end users (that user's own
// access token), so it holds no more than the page would (tools_probe_test.go).
var allowedRoutes = []*regexp.Regexp{
	regexp.MustCompile(`^GET /api/provision/$`),
	regexp.MustCompile(`^GET /api/projects/[^/]+/info/$`),
	// Publishable SDK keys only: they ship inside client apps and grant what
	// the anon role grants. Secret keys and personal access tokens stay out.
	regexp.MustCompile(`^(GET|POST) /api/projects/[^/]+/sdk-keys/$`),
	regexp.MustCompile(`^GET /api/schema/[^/]+/(tables|relationships)(\?.*)?$`),
	regexp.MustCompile(`^GET /api/schema/[^/]+/tables/[^/]+/(columns|indexes|checks)(\?.*)?$`),
	regexp.MustCompile(`^(GET|POST) /api/schema/[^/]+/query(\?.*)?$`),
	regexp.MustCompile(`^(GET|POST) /api/provision/[^/]+/migrations/$`),
	regexp.MustCompile(`^GET /api/provision/[^/]+/permissions/$`),
	regexp.MustCompile(`^(PUT|DELETE) /api/provision/[^/]+/permissions/tables/[^/]+/roles/[^/]+/[^/]+$`),
	regexp.MustCompile(`^GET /api/provision/[^/]+/logs(\?.*)?$`),
	regexp.MustCompile(`^(GET|POST) /api/projects/[^/]+/functions/$`),
	regexp.MustCompile(`^DELETE /api/projects/[^/]+/functions/[^/]+/$`),
	// Which outside hosts the project's functions may reach.
	regexp.MustCompile(`^(GET|PUT) /api/projects/[^/]+/functions/egress$`),
	regexp.MustCompile(`^GET /api/projects/[^/]+/realtime/tables$`),
	regexp.MustCompile(`^(PUT|DELETE) /api/projects/[^/]+/realtime/tables/[^/]+/[^/]+$`),
	// Serving a database function through the API, and who may call it.
	regexp.MustCompile(`^POST /api/provision/[^/]+/tracked-functions/$`),
	regexp.MustCompile(`^DELETE /api/provision/[^/]+/tracked-functions/[^/]+$`),
	regexp.MustCompile(`^(PUT|DELETE) /api/provision/[^/]+/function-permissions/[^/]+/roles/[^/]+$`),
	regexp.MustCompile(`^POST /api/projects/[^/]+/functions/secrets$`),
	regexp.MustCompile(`^GET /api/projects/[^/]+/functions/[^/]+/logs(\?.*)?$`),
	regexp.MustCompile(`^(GET|POST) /api/projects/[^/]+/apps/$`),
	// The allowlist is read whole and edited one origin at a time; the
	// wildcard is never set through MCP.
	regexp.MustCompile(`^GET /api/projects/[^/]+/cors/$`),
	regexp.MustCompile(`^(POST|DELETE) /api/projects/[^/]+/cors/origins(\?.*)?$`),
	regexp.MustCompile(`^GET /api/projects/[^/]+/apps/[^/]+/certificate$`),
	regexp.MustCompile(`^GET /api/projects/[^/]+/apps/[^/]+/$`),
	regexp.MustCompile(`^POST /api/projects/[^/]+/apps/[^/]+/deploy$`),
	regexp.MustCompile(`^GET /api/projects/[^/]+/apps/[^/]+/(deploys|deploys/[^/]+|logs)(\?.*)?$`),
}

// neverReached is the surface ADR 0037 keeps out of MCP: project lifecycle,
// members and roles, tiers, sign-in providers, backups and restores,
// credentials and token minting.
var neverReached = []*regexp.Regexp{
	regexp.MustCompile(`^(POST|DELETE) /api/provision/([^/]+/?)?$`),
	regexp.MustCompile(`/database$`),
	regexp.MustCompile(`/credentials`),
	regexp.MustCompile(`/members`),
	regexp.MustCompile(`/invites`),
	regexp.MustCompile(`/end-users`),
	regexp.MustCompile(`/tier`),
	regexp.MustCompile(`/sso-providers`),
	regexp.MustCompile(`/backup`),
	regexp.MustCompile(`/snapshot`),
	regexp.MustCompile(`/restore`),
	regexp.MustCompile(`/purge`),
	regexp.MustCompile(`/deletion`),
	regexp.MustCompile(`/pause|/resume`),
	regexp.MustCompile(`/api/auth/`),
	regexp.MustCompile(`/api/vault`),
	regexp.MustCompile(`/api/admin`),
	regexp.MustCompile(`/api/orgs`),
	regexp.MustCompile(`/documentdb/users`),
	regexp.MustCompile(`/registry-credentials`),
}

func TestToolCatalogueIsExactlyTheCodingSurface(t *testing.T) {
	cs := session(t, newFakeRoutes(), &recordingAudit{}, writeCaller())
	listed, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	got := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		got = append(got, tool.Name)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(offeredTools, ",") {
		t.Fatalf("tools:\n%v\nwant:\n%v", got, offeredTools)
	}
	forbidden := regexp.MustCompile(`project_(create|delete)|create_project|delete_project|member|role_grant|tier|sso|sign_in|backup|restore|purge|credential|rotate|token|pause|resume`)
	for _, name := range got {
		if forbidden.MatchString(name) {
			t.Errorf("%s names a capability MCP must never offer", name)
		}
	}
}

func TestEveryOfferedToolHasARouteCase(t *testing.T) {
	covered := map[string]bool{}
	for _, tc := range routeCases() {
		covered[tc.tool] = true
	}
	for _, name := range offeredTools {
		if !covered[name] {
			t.Errorf("%s has no routing case", name)
		}
	}
}

func TestNoToolLeavesItsAllowedRoutes(t *testing.T) {
	for _, tc := range routeCases() {
		for _, call := range tc.expect {
			for _, never := range neverReached {
				if never.MatchString(call) {
					t.Errorf("%s calls %s, which MCP must never reach", tc.tool, call)
				}
			}
			if !matchesAny(allowedRoutes, call) {
				t.Errorf("%s calls %s, which is not an allowed route", tc.tool, call)
			}
		}
	}
}

func matchesAny(patterns []*regexp.Regexp, value string) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}
