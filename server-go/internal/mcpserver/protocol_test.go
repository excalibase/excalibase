package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	rawWriteToken   = "excb_write_token"
	rawReadToken    = "excb_read_token"
	rawSessionToken = "excb_session_token"
)

// knownTokens resolves the three credentials the protocol test presents.
type knownTokens struct{}

func (knownTokens) FindByTokenHash(_ context.Context, hash string) (*domain.AccessToken, error) {
	tokens := map[string]domain.AccessToken{
		auth.HashToken(rawWriteToken):   {Name: "ci", Scopes: auth.ScopeWrite},
		auth.HashToken(rawReadToken):    {Name: "reader", Scopes: auth.ScopeRead},
		auth.HashToken(rawSessionToken): {Name: "session", Scopes: auth.ScopeSession},
	}
	token, ok := tokens[hash]
	if !ok {
		return nil, nil
	}
	token.TokenHash, token.UserID = hash, testUserID
	return &token, nil
}

func (knownTokens) FindUserByID(_ context.Context, id string) (*domain.User, error) {
	return &domain.User{ID: id, Active: true}, nil
}

// bearer adds the token to every request the MCP client sends.
type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

func protocolServer(t *testing.T) *httptest.Server {
	t.Helper()
	r := chi.NewRouter()
	r.Use(auth.ExtractAuth(knownTokens{}))
	r.With(auth.RequireAuth).Get("/api/schema/{projectId}/tables", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"orders","schema":"public","type":"table"}]`))
	})
	r.Handle("/mcp", NewHandler(r, &recordingAudit{}, testSettings))
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)
	return server
}

func connectOverHTTP(t *testing.T, endpoint, token string) (*mcp.ClientSession, error) {
	t.Helper()
	transport := &mcp.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: &http.Client{Transport: bearer{token: token, next: http.DefaultTransport}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "protocol-test", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), transport, nil)
	if err == nil {
		t.Cleanup(func() { _ = cs.Close() })
	}
	return cs, err
}

func TestMCPOverStreamableHTTP(t *testing.T) {
	server := protocolServer(t)
	cs, err := connectOverHTTP(t, server.URL+"/mcp", rawWriteToken)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	listed, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(listed.Tools) != len(offeredTools) {
		t.Errorf("listed %d tools, want %d", len(listed.Tools), len(offeredTools))
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "list_tables", Arguments: map[string]any{"project_id": testProjectA},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	out := structured(t, res)
	if tables, _ := out["tables"].([]any); len(tables) != 1 {
		t.Fatalf("tables = %v", out["tables"])
	}
}

func TestMCPOverHTTPReadOnlyNarrows(t *testing.T) {
	server := protocolServer(t)
	for name, endpoint := range map[string]string{
		"write token asks read_only": server.URL + "/mcp?read_only=true",
		"read token":                 server.URL + "/mcp",
	} {
		t.Run(name, func(t *testing.T) {
			token := rawWriteToken
			if name == "read token" {
				token = rawReadToken
			}
			cs, err := connectOverHTTP(t, endpoint, token)
			if err != nil {
				t.Fatalf("connect: %v", err)
			}
			listed, err := cs.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatalf("list tools: %v", err)
			}
			for _, tool := range listed.Tools {
				if !tool.Annotations.ReadOnlyHint {
					t.Errorf("%s offered on a read-only connection", tool.Name)
				}
			}
		})
	}
}

func TestMCPOverHTTPRefusesWhatIsNotAPersonalAccessToken(t *testing.T) {
	server := protocolServer(t)
	for name, token := range map[string]string{"no token": "", "unknown token": "nope", "session token": rawSessionToken} {
		t.Run(name, func(t *testing.T) {
			if _, err := connectOverHTTP(t, server.URL+"/mcp", token); err == nil {
				t.Fatalf("connected without a personal access token")
			}
		})
	}
	resp, err := http.Post(server.URL+"/mcp", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatalf("status %d, WWW-Authenticate %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
}
