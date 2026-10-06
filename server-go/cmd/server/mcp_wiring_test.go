package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	mcpViewerID = "mcp-viewer-a"
	mcpDevPAT   = "dev-pat"
	mcpBoundPAT = "dev-bound-pat"
	mcpReadPAT  = "dev-read-pat"
	mcpViewPAT  = "viewer-pat"
)

// discardAudit keeps the entries the MCP endpoint writes.
type discardAudit struct {
	mu      sync.Mutex
	entries []domain.AuditEntry
}

func (a *discardAudit) LogAudit(_ context.Context, entry *domain.AuditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, *entry)
	return nil
}

// mcpServer is the production router, as policyRouter builds it, with
// personal access tokens for a developer and a viewer of org A.
func mcpServer(t *testing.T) (*httptest.Server, *discardAudit) {
	t.Helper()
	instances := fakestore.NewInstances()
	instances.Items[matrixProjectA] = &domain.DatabaseInstance{ProjectID: matrixProjectA, OrgID: matrixOrgA, Status: "ACTIVE"}
	instances.Items[matrixProjectB] = &domain.DatabaseInstance{ProjectID: matrixProjectB, OrgID: matrixOrgB, Status: "ACTIVE"}
	platform := &fakePlatform{Orgs: fakestore.NewOrgs(), Tokens: fakestore.NewTokens()}
	platform.AddMember(matrixOrgA, matrixDevID, domain.OrgRoleDeveloper)
	platform.AddMember(matrixOrgA, mcpViewerID, domain.OrgRoleViewer)
	add := func(name, userID string, token domain.AccessToken) {
		platform.Users[userID] = &domain.User{ID: userID, Role: "user", Active: true}
		token.TokenHash, token.UserID, token.Name = auth.HashToken(testutil.FixtureToken(name)), userID, name
		platform.ByHash[token.TokenHash] = &token
	}
	add(mcpDevPAT, matrixDevID, domain.AccessToken{Scopes: auth.ScopeWrite})
	add(mcpBoundPAT, matrixDevID, domain.AccessToken{Scopes: auth.ScopeWrite, ProjectID: matrixProjectA})
	add(mcpReadPAT, matrixDevID, domain.AccessToken{Scopes: auth.ScopeRead})
	add(mcpViewPAT, mcpViewerID, domain.AccessToken{Scopes: auth.ScopeWrite})

	functions := edgefn.NewFunctionStore(t.TempDir())
	seedPolicyFunction(t, functions)
	deps := policyDeps(t, instances, platform, functions)
	audit := &discardAudit{}
	deps.mcpAudit = audit
	cfg := config.AppConfig{DeploymentMode: "cloud", AppHostingEnabled: true, PublicBaseURL: "https://api.example.test"}
	server := httptest.NewServer(buildRouter(cfg, platform, instances, deps))
	t.Cleanup(server.Close)
	return server, audit
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func mcpConnect(t *testing.T, endpoint, tokenName string) (*mcp.ClientSession, error) {
	t.Helper()
	transport := &mcp.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: &http.Client{Transport: bearerTransport{token: testutil.FixtureToken(tokenName)}},
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "wiring-test", Version: "1"}, nil).
		Connect(context.Background(), transport, nil)
	if err == nil {
		t.Cleanup(func() { _ = cs.Close() })
	}
	return cs, err
}

func mcpCall(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
}

func mcpText(res *mcp.CallToolResult) string {
	var parts []string
	for _, content := range res.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// TestMCPToolsMeetTheRealGates drives the production router: a tool reaches
// what the caller may reach and nothing else, refused by the same gates.
func TestMCPToolsMeetTheRealGates(t *testing.T) {
	server, audit := mcpServer(t)

	dev, err := mcpConnect(t, server.URL+"/mcp", mcpDevPAT)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	res := mcpCall(t, dev, "list_functions", map[string]any{"project_id": matrixProjectA})
	if res.IsError || !strings.Contains(mcpText(res), `"id":"x"`) {
		t.Fatalf("developer listing own project's functions: %s", mcpText(res))
	}
	res = mcpCall(t, dev, "list_functions", map[string]any{"project_id": matrixProjectB})
	if !res.IsError || !strings.Contains(mcpText(res), "404") {
		t.Fatalf("another org's project must read as absent: %s", mcpText(res))
	}

	viewer, err := mcpConnect(t, server.URL+"/mcp", mcpViewPAT)
	if err != nil {
		t.Fatalf("connect viewer: %v", err)
	}
	res = mcpCall(t, viewer, "apply_migration", map[string]any{"project_id": matrixProjectA, "name": "x", "sql": "select 1"})
	if !res.IsError || !strings.Contains(mcpText(res), "insufficient project role") {
		t.Fatalf("a viewer must be refused a migration by the role gate: %s", mcpText(res))
	}

	if len(audit.entries) != 3 || audit.entries[0].Via != domain.AuditViaMCP || audit.entries[0].ProjectID != matrixProjectA {
		t.Fatalf("audit = %+v", audit.entries)
	}
}

func TestMCPNarrowingOverTheRealRouter(t *testing.T) {
	server, _ := mcpServer(t)

	// Pointed at another project, the connection answers every call with why.
	other, err := mcpConnect(t, server.URL+"/mcp?project="+matrixProjectB, mcpBoundPAT)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if res := mcpCall(t, other, "list_functions", map[string]any{"project_id": matrixProjectB}); !res.IsError || !strings.Contains(mcpText(res), "bound to another project") {
		t.Fatalf("a token bound to one project reached another: %s", mcpText(res))
	}

	bound, err := mcpConnect(t, server.URL+"/mcp", mcpBoundPAT)
	if err != nil {
		t.Fatalf("connect bound: %v", err)
	}
	if res := mcpCall(t, bound, "list_functions", map[string]any{}); res.IsError {
		t.Fatalf("a bound connection fills in its own project: %s", mcpText(res))
	}

	for name, endpoint := range map[string]string{
		"write token asking read_only": server.URL + "/mcp?read_only=true",
		"read token asking writes":     server.URL + "/mcp?read_only=false",
	} {
		t.Run(name, func(t *testing.T) {
			token := mcpDevPAT
			if strings.Contains(endpoint, "false") {
				token = mcpReadPAT
			}
			cs, err := mcpConnect(t, endpoint, token)
			if err != nil {
				t.Fatalf("connect: %v", err)
			}
			listed, err := cs.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			for _, tool := range listed.Tools {
				if tool.Name == "deploy_function" || tool.Name == "apply_migration" {
					t.Errorf("%s offered on a read-only connection", tool.Name)
				}
			}
		})
	}
}
