package auth

import (
	"net/http"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestTokenBoundToProject(t *testing.T) {
	cases := []struct {
		name    string
		token   *domain.AccessToken
		project string
		want    bool
	}{
		{"no token (session-less caller) is not restricted", nil, "p1", true},
		{"unbound token reaches any project", &domain.AccessToken{}, "p1", true},
		{"bound token reaches its own project", &domain.AccessToken{ProjectID: "p1"}, "p1", true},
		{"bound token refused on another project", &domain.AccessToken{ProjectID: "p1"}, "p2", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TokenBoundToProject(tc.token, tc.project); got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestTokenAllowsMethod(t *testing.T) {
	cases := []struct {
		name   string
		scopes string
		method string
		want   bool
	}{
		{"no token allows reads", "", http.MethodGet, true},
		{"legacy empty scopes allow writes", "", http.MethodPost, true},
		{"session allows writes", ScopeSession, http.MethodDelete, true},
		{"admin allows writes", ScopeAdmin, http.MethodPut, true},
		{"write allows writes", ScopeWrite, http.MethodPatch, true},
		{"read allows GET", ScopeRead, http.MethodGet, true},
		{"read allows HEAD", ScopeRead, http.MethodHead, true},
		{"read refuses POST", ScopeRead, http.MethodPost, false},
		{"read refuses DELETE", ScopeRead, http.MethodDelete, false},
		{"read,write allows POST", "read, write", http.MethodPost, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var tok *domain.AccessToken
			if tc.scopes != "" {
				tok = &domain.AccessToken{Scopes: tc.scopes}
			}
			if got := TokenAllowsMethod(tok, tc.method); got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestNormalizeScopes(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		want    string
		wantErr bool
	}{
		{"empty stays empty", nil, "", false},
		{"read only", []string{"read"}, "read", false},
		{"dedup and trim", []string{" read", "write", "read"}, "read,write", false},
		{"unknown scope refused", []string{"root"}, "", true},
		{"session is reserved for login", []string{"session"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeScopes(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}
