package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

const (
	testUserID   = "user-1"
	testProjectA = "proj-a"
	testProjectB = "proj-b"
	testRawToken = "excb_test_secret"
)

func authedRequest(target string, token *domain.AccessToken) *http.Request {
	req := httptest.NewRequest(http.MethodPost, target, nil)
	req.Header.Set("Authorization", "Bearer "+testRawToken)
	ctx := auth.SetUser(req.Context(), &domain.User{ID: testUserID, Active: true})
	if token != nil {
		ctx = auth.SetToken(ctx, token)
	}
	return req.WithContext(ctx)
}

func TestConnectNarrowsOnlyEverNarrows(t *testing.T) {
	cases := []struct {
		name        string
		target      string
		token       domain.AccessToken
		wantRO      bool
		wantProject string
		wantStatus  int
	}{
		{"write PAT, no params", "/mcp", domain.AccessToken{Scopes: auth.ScopeWrite}, false, "", 0},
		{"write PAT asks read_only", "/mcp?read_only=true", domain.AccessToken{Scopes: auth.ScopeWrite}, true, "", 0},
		{"read PAT is read-only without asking", "/mcp", domain.AccessToken{Scopes: auth.ScopeRead}, true, "", 0},
		{"read PAT cannot ask for writes", "/mcp?read_only=false", domain.AccessToken{Scopes: auth.ScopeRead}, true, "", 0},
		{"legacy all-purpose PAT asks read_only", "/mcp?read_only=true", domain.AccessToken{}, true, "", 0},
		{"unbound PAT narrowed to a project", "/mcp?project=" + testProjectA, domain.AccessToken{Scopes: auth.ScopeWrite}, false, testProjectA, 0},
		{"bound PAT keeps its project", "/mcp", domain.AccessToken{Scopes: auth.ScopeWrite, ProjectID: testProjectA}, false, testProjectA, 0},
		{"bound PAT names its own project", "/mcp?project=" + testProjectA, domain.AccessToken{ProjectID: testProjectA}, false, testProjectA, 0},
		{"bound PAT cannot widen to another project", "/mcp?project=" + testProjectB, domain.AccessToken{ProjectID: testProjectA}, false, "", http.StatusForbidden},
		{"read_only must be a boolean", "/mcp?read_only=maybe", domain.AccessToken{Scopes: auth.ScopeWrite}, false, "", http.StatusBadRequest},
		{"project must be a valid id", "/mcp?project=../etc", domain.AccessToken{Scopes: auth.ScopeWrite}, false, "", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := tc.token
			token.TokenHash = "hash"
			token.Name = "laptop"
			caller, refusal := connect(authedRequest(tc.target, &token))
			if tc.wantStatus != 0 {
				if refusal == nil || refusal.status != tc.wantStatus {
					t.Fatalf("refusal = %+v, want status %d", refusal, tc.wantStatus)
				}
				return
			}
			if refusal != nil {
				t.Fatalf("unexpected refusal %+v", refusal)
			}
			if caller.ReadOnly != tc.wantRO {
				t.Errorf("ReadOnly = %v, want %v", caller.ReadOnly, tc.wantRO)
			}
			if caller.Project != tc.wantProject {
				t.Errorf("Project = %q, want %q", caller.Project, tc.wantProject)
			}
			if caller.Token.ProjectID != tc.wantProject {
				t.Errorf("narrowed token ProjectID = %q, want %q", caller.Token.ProjectID, tc.wantProject)
			}
			if tc.wantRO && auth.TokenAllowsMethod(caller.Token, http.MethodPost) {
				t.Errorf("a read-only connection must carry a token the router refuses writes for")
			}
			if !tc.wantRO && !auth.TokenAllowsMethod(caller.Token, http.MethodPost) {
				t.Errorf("a read-write connection lost its write scope")
			}
			if token.Scopes != tc.token.Scopes || token.ProjectID != tc.token.ProjectID {
				t.Errorf("connect mutated the stored token: %+v", token)
			}
		})
	}
}

func TestConnectRefusesWhatIsNotAPersonalAccessToken(t *testing.T) {
	t.Run("anonymous", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		_, refusal := connect(req)
		if refusal == nil || refusal.status != http.StatusUnauthorized {
			t.Fatalf("refusal = %+v, want 401", refusal)
		}
	})
	t.Run("session cookie without a bearer header", func(t *testing.T) {
		req := authedRequest("/mcp", &domain.AccessToken{Scopes: auth.ScopeSession})
		req.Header.Del("Authorization")
		_, refusal := connect(req)
		if refusal == nil || refusal.status != http.StatusUnauthorized {
			t.Fatalf("refusal = %+v, want 401", refusal)
		}
	})
	t.Run("session token as a bearer", func(t *testing.T) {
		_, refusal := connect(authedRequest("/mcp", &domain.AccessToken{Scopes: auth.ScopeSession}))
		if refusal == nil || refusal.status != http.StatusForbidden {
			t.Fatalf("refusal = %+v, want 403", refusal)
		}
	})
	t.Run("capability token", func(t *testing.T) {
		_, refusal := connect(authedRequest("/mcp", &domain.AccessToken{Permissions: []string{"policies:read"}}))
		if refusal == nil || refusal.status != http.StatusForbidden {
			t.Fatalf("refusal = %+v, want 403", refusal)
		}
	})
}

func TestResolveProject(t *testing.T) {
	bound := Caller{Project: testProjectA}
	unbound := Caller{}
	cases := []struct {
		name      string
		caller    Caller
		requested string
		want      string
		wantErr   bool
	}{
		{"bound connection fills the project", bound, "", testProjectA, false},
		{"bound connection accepts its own project", bound, testProjectA, testProjectA, false},
		{"bound connection refuses another project", bound, testProjectB, "", true},
		{"unbound connection takes the named project", unbound, testProjectB, testProjectB, false},
		{"unbound connection needs a project", unbound, "", "", true},
		{"project id is validated", unbound, "a/b", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.caller.resolveProject(tc.requested)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}
