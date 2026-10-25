package auth

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-657: a metrics key is a PAT holding the metrics:read scope.
func TestIsMetricsKey(t *testing.T) {
	cases := []struct {
		scopes string
		want   bool
	}{
		{ScopeMetricsRead, true},
		{"read," + ScopeMetricsRead, true},
		{"", false},
		{ScopeRead, false},
		{"read,write", false},
		{ScopeSession, false},
	}
	for _, tc := range cases {
		if got := IsMetricsKey(&domain.AccessToken{Scopes: tc.scopes}); got != tc.want {
			t.Errorf("scopes %q: %v, want %v", tc.scopes, got, tc.want)
		}
	}
	if IsMetricsKey(nil) {
		t.Error("no token is no metrics key")
	}
}

// Only the project's metrics-keys route mints one, behind its Admin gate.
func TestNormalizeScopes_RefusesTheMetricsScope(t *testing.T) {
	if _, err := NormalizeScopes([]string{ScopeMetricsRead}); err == nil {
		t.Error("a PAT request may not ask for metrics:read")
	}
}

// A metrics key never counts as a legacy all-purpose token.
func TestMetricsKey_IsRestricted(t *testing.T) {
	key := &domain.AccessToken{Scopes: ScopeMetricsRead, ProjectID: "p1"}
	if IsUnrestrictedCredential(key) || TokenMayRevealSecrets(key) || TokenAllowsMethod(key, "POST") {
		t.Error("a metrics key is read-only and narrowed")
	}
}
