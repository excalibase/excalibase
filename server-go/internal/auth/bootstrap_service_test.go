package auth

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

// memTokens is a TokenStore keyed by hash.
type memTokens struct {
	byHash map[string]*domain.AccessToken
}

func newMemTokens() *memTokens { return &memTokens{byHash: map[string]*domain.AccessToken{}} }

func (s *memTokens) CreateToken(_ context.Context, t *domain.AccessToken) error {
	s.byHash[t.TokenHash] = t
	return nil
}
func (s *memTokens) FindByTokenHash(_ context.Context, hash string) (*domain.AccessToken, error) {
	return s.byHash[hash], nil
}
func (s *memTokens) ListTokensByUser(_ context.Context, userID string) ([]*domain.AccessToken, error) {
	var out []*domain.AccessToken
	for _, t := range s.byHash {
		if t.UserID == userID {
			out = append(out, t)
		}
	}
	return out, nil
}
func (s *memTokens) DeleteToken(_ context.Context, hash string) error {
	delete(s.byHash, hash)
	return nil
}
func (s *memTokens) UpdateTokenExpiry(context.Context, string, *time.Time) error { return nil }
func (s *memTokens) TouchTokenLastUsed(context.Context, string, time.Time) error { return nil }

const testBootstrapToken = "bootstrap-token-0123456789abcdef0123456789abcdef"

var testBootstrapPermissions = []string{"service-tokens:manage:svc-auth", "vault:init"}

func principal(t *testing.T, users *fakestore.Users) *domain.User {
	t.Helper()
	u, _ := users.FindUserByUsername(context.Background(), BootstrapServiceName)
	if u == nil {
		t.Fatal("svc-bootstrap principal was not created")
	}
	return u
}

func TestAdoptBootstrapServiceTokenCreatesThePrincipalAndItsToken(t *testing.T) {
	users, tokens := fakestore.NewUsers(), newMemTokens()

	if err := AdoptBootstrapServiceToken(context.Background(), users, tokens, testBootstrapToken, testBootstrapPermissions); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	u := principal(t, users)
	if !u.IsService() || u.PasswordHash != "" {
		t.Fatalf("svc-bootstrap must be a password-less service principal: %+v", u)
	}
	tok := tokens.byHash[HashToken(testBootstrapToken)]
	if tok == nil || tok.UserID != u.ID {
		t.Fatal("the supplied token must authenticate as svc-bootstrap")
	}
	if !IsCapabilityToken(tok) || tok.ExpiresAt != nil {
		t.Fatalf("token must be a non-expiring capability token: %+v", tok)
	}
	want, _ := NormalizeCapabilities(testBootstrapPermissions)
	if !reflect.DeepEqual(tok.Permissions, want) {
		t.Fatalf("permissions = %v, want %v", tok.Permissions, want)
	}
}

func TestAdoptBootstrapServiceTokenIsIdempotentAndFollowsThePermissionList(t *testing.T) {
	users, tokens := fakestore.NewUsers(), newMemTokens()
	ctx := context.Background()
	if err := AdoptBootstrapServiceToken(ctx, users, tokens, testBootstrapToken, testBootstrapPermissions); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	narrower := []string{"vault:init"}
	if err := AdoptBootstrapServiceToken(ctx, users, tokens, testBootstrapToken, narrower); err != nil {
		t.Fatalf("re-adopt: %v", err)
	}
	if len(tokens.byHash) != 1 {
		t.Fatalf("re-adoption must not duplicate the token: %d tokens", len(tokens.byHash))
	}
	if got := tokens.byHash[HashToken(testBootstrapToken)].Permissions; !reflect.DeepEqual(got, narrower) {
		t.Fatalf("permissions = %v, want the new list %v", got, narrower)
	}
}

func TestAdoptBootstrapServiceTokenRetiresAReplacedToken(t *testing.T) {
	users, tokens := fakestore.NewUsers(), newMemTokens()
	ctx := context.Background()
	if err := AdoptBootstrapServiceToken(ctx, users, tokens, testBootstrapToken, testBootstrapPermissions); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	replacement := strings.Repeat("r", 48)
	if err := AdoptBootstrapServiceToken(ctx, users, tokens, replacement, testBootstrapPermissions); err != nil {
		t.Fatalf("adopt replacement: %v", err)
	}
	if tokens.byHash[HashToken(testBootstrapToken)] != nil {
		t.Fatal("replacing the Secret must retire the old svc-bootstrap token")
	}
	if tokens.byHash[HashToken(replacement)] == nil {
		t.Fatal("the replacement token must be adopted")
	}
}

func TestAdoptBootstrapServiceTokenRefusals(t *testing.T) {
	cases := map[string]struct {
		raw         string
		permissions []string
		seed        *domain.User
	}{
		"short token":            {raw: "short", permissions: testBootstrapPermissions},
		"no permissions":         {raw: testBootstrapToken},
		"malformed permission":   {raw: testBootstrapToken, permissions: []string{"not a capability"}},
		"name taken by a person": {raw: testBootstrapToken, permissions: testBootstrapPermissions, seed: &domain.User{ID: "h", Username: BootstrapServiceName, Kind: domain.UserKindHuman}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			users, tokens := fakestore.NewUsers(), newMemTokens()
			if tc.seed != nil {
				users.Add(tc.seed)
			}
			if err := AdoptBootstrapServiceToken(context.Background(), users, tokens, tc.raw, tc.permissions); err == nil {
				t.Fatal("expected a refusal")
			}
			if len(tokens.byHash) != 0 {
				t.Fatal("a refused adoption must not mint anything")
			}
		})
	}
}

func TestAdoptBootstrapServiceTokenIsANoOpWhenUnset(t *testing.T) {
	users, tokens := fakestore.NewUsers(), newMemTokens()
	if err := AdoptBootstrapServiceToken(context.Background(), users, tokens, "", nil); err != nil {
		t.Fatalf("unset: %v", err)
	}
	if len(users.ByID) != 0 {
		t.Fatal("nothing must be created when no bootstrap token is configured")
	}
}
