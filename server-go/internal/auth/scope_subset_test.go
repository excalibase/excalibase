package auth

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestIsSessionToken(t *testing.T) {
	cases := []struct {
		name  string
		token *domain.AccessToken
		want  bool
	}{
		{"no token", nil, false},
		{"login session", &domain.AccessToken{Scopes: ScopeSession}, true},
		{"read-only PAT", &domain.AccessToken{Scopes: ScopeRead}, false},
		{"all-purpose PAT", &domain.AccessToken{Scopes: ""}, false},
		{"write PAT", &domain.AccessToken{Scopes: "read,write"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSessionToken(tc.token); got != tc.want {
				t.Errorf("IsSessionToken = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsUnrestrictedCredential(t *testing.T) {
	cases := []struct {
		name  string
		token *domain.AccessToken
		want  bool
	}{
		{"no token", nil, false},
		{"login session", &domain.AccessToken{Scopes: ScopeSession}, true},
		{"legacy all-purpose PAT", &domain.AccessToken{}, true},
		{"read-only PAT", &domain.AccessToken{Scopes: ScopeRead}, false},
		{"write PAT", &domain.AccessToken{Scopes: "read,write"}, false},
		{"project-bound but unscoped PAT", &domain.AccessToken{ProjectID: "proj-a"}, false},
		{"capability token", &domain.AccessToken{Permissions: []string{"policies:read"}}, false},
		{"session carrying permissions is still a capability token",
			&domain.AccessToken{Scopes: ScopeSession, Permissions: []string{"policies:read"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsUnrestrictedCredential(tc.token); got != tc.want {
				t.Errorf("IsUnrestrictedCredential = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestScopesSubsetOf(t *testing.T) {
	cases := []struct {
		name              string
		requested, holder string
		want              bool
	}{
		{"all-purpose holder grants anything", "read,write", "", true},
		{"all-purpose holder grants all-purpose", "", "", true},
		{"restricted holder cannot grant all-purpose", "", "read", false},
		{"same scope", "read", "read", true},
		{"narrower", "read", "read,write", true},
		{"wider", "read,write", "read", false},
		{"unrelated scope", "admin", "read,write", false},
		{"order and spacing do not matter", "write, read", "read,write", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ScopesSubsetOf(tc.requested, tc.holder); got != tc.want {
				t.Errorf("ScopesSubsetOf(%q, %q) = %v, want %v", tc.requested, tc.holder, got, tc.want)
			}
		})
	}
}

func TestProjectBindingWithin(t *testing.T) {
	cases := []struct {
		name              string
		requested, holder string
		want              bool
	}{
		{"unbound holder may bind anywhere", "proj-a", "", true},
		{"unbound holder may stay unbound", "", "", true},
		{"bound holder keeps its project", "proj-a", "proj-a", true},
		{"bound holder may not unbind", "", "proj-a", false},
		{"bound holder may not rebind", "proj-b", "proj-a", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProjectBindingWithin(tc.requested, tc.holder); got != tc.want {
				t.Errorf("ProjectBindingWithin(%q, %q) = %v, want %v", tc.requested, tc.holder, got, tc.want)
			}
		})
	}
}
