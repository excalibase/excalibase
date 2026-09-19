package handler

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

const (
	subsetProjectA = "subset-proj-a"
	subsetProjectB = "subset-proj-b"
	subsetOrg      = "subset-org"
	subsetUserID   = "subset-u1"
)

// subsetFixture mounts token creation and rotation behind the real auth
// middleware, so a test can authenticate as a specific PAT or session.
type subsetFixture struct {
	router chi.Router
	tokens *mockTokenStore
	users  *mockUserStore
}

func newSubsetFixture(t *testing.T, role string) *subsetFixture {
	t.Helper()
	users, tokens := newMockUserStore(), newMockTokenStore()
	now := time.Now()
	users.users[subsetUserID] = &domain.User{
		ID: subsetUserID, Username: testutil.FixtureToken("subset"),
		Role: role, Active: true, Kind: domain.UserKindHuman, CreatedAt: &now,
	}

	instances := fakestore.NewInstances()
	instances.Save(&domain.DatabaseInstance{ProjectID: subsetProjectA, OrgID: subsetOrg})
	instances.Save(&domain.DatabaseInstance{ProjectID: subsetProjectB, OrgID: subsetOrg})
	orgs := fakestore.NewOrgs()
	orgs.AddMember(subsetOrg, subsetUserID, domain.OrgRoleAdmin)

	handler := NewAuthHandler(users, tokens)
	handler.SetOrgStore(orgs)
	handler.SetInstanceStore(instances)

	router := chi.NewRouter()
	router.Use(auth.ExtractAuth(&storeLookup{us: users, ts: tokens}))
	router.With(auth.RequireAuth).Route(routeTokens, func(r chi.Router) {
		r.Post("/", handler.CreateToken)
		r.Post("/{tokenHash}"+rotateSuffix, handler.RotateToken)
	})
	return &subsetFixture{router: router, tokens: tokens, users: users}
}

// authenticateAs stores a token for the fixture's user and returns its raw
// secret, so a request can be made as that exact credential.
func (f *subsetFixture) authenticateAs(name, scopes, projectID string) string {
	raw := testutil.FixtureToken(name)
	now := time.Now()
	f.tokens.tokens[auth.HashToken(raw)] = &domain.AccessToken{
		TokenHash: auth.HashToken(raw), TokenPrefix: auth.TokenPrefix(raw),
		UserID: subsetUserID, Name: name, Scopes: scopes, ProjectID: projectID, CreatedAt: &now,
	}
	return raw
}

// TestCreateToken_PATCannotWidenScopes reproduces EXC-396: a read-only PAT
// bound to project A must not mint a write-capable, unbound token.
func TestCreateToken_PATCannotWidenScopes(t *testing.T) {
	fixture := newSubsetFixture(t, "user")
	pat := fixture.authenticateAs("ci-read-a", "read", subsetProjectA)

	w := doAuthRequest(fixture.router, http.MethodPost, routeTokens+"/", pat,
		`{"name":"escape","scopes":["read","write"]}`)

	if w.Code != http.StatusForbidden {
		t.Errorf("PAT widening scopes: got %d, want 403 (body: %s)", w.Code, w.Body.String())
	}
	if len(fixture.tokens.tokens) != 1 {
		t.Errorf("refused request persisted a token: %d stored, want 1 (the caller's)", len(fixture.tokens.tokens))
	}
}

// TestCreateToken_PATSubsetRules covers the whole rule table.
func TestCreateToken_PATSubsetRules(t *testing.T) {
	cases := []struct {
		name          string
		callerScopes  string
		callerProject string
		body          string
		want          int
	}{
		{"widen scopes", "read", subsetProjectA, `{"name":"n","scopes":["read","write"]}`, http.StatusForbidden},
		{"drop the project binding", "read", subsetProjectA, `{"name":"n","scopes":["read"]}`, http.StatusForbidden},
		// A bound token cannot see another project at all, so the existing
		// visibility check answers first — and 404 is the better answer: it
		// does not confirm that project B exists.
		{"rebind to another project", "read", subsetProjectA, `{"name":"n","projectId":"` + subsetProjectB + `","scopes":["read"]}`, http.StatusNotFound},
		{"become all-purpose", "read", "", `{"name":"n"}`, http.StatusForbidden},
		{"same binding and scopes", "read", subsetProjectA, `{"name":"n","projectId":"` + subsetProjectA + `","scopes":["read"]}`, http.StatusCreated},
		{"narrower scopes", "read,write", subsetProjectA, `{"name":"n","projectId":"` + subsetProjectA + `","scopes":["read"]}`, http.StatusCreated},
		{"unbound caller binds a visible project", "read", "", `{"name":"n","projectId":"` + subsetProjectA + `","scopes":["read"]}`, http.StatusCreated},
		{"all-purpose caller mints anything", "", "", `{"name":"n","scopes":["read","write"]}`, http.StatusCreated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSubsetFixture(t, "user")
			pat := fixture.authenticateAs("caller", tc.callerScopes, tc.callerProject)
			w := doAuthRequest(fixture.router, http.MethodPost, routeTokens+"/", pat, tc.body)
			if w.Code != tc.want {
				t.Errorf("got %d, want %d (body: %s)", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// TestCreateToken_SessionIsUnrestricted keeps the studio path working: the
// user acting directly in a browser may mint any PAT they are entitled to.
func TestCreateToken_SessionIsUnrestricted(t *testing.T) {
	fixture := newSubsetFixture(t, "user")
	session := fixture.authenticateAs("login", auth.ScopeSession, "")

	w := doAuthRequest(fixture.router, http.MethodPost, routeTokens+"/", session,
		`{"name":"ci","projectId":"`+subsetProjectA+`","scopes":["read","write"]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("session minting a PAT: got %d, want 201 (body: %s)", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if got, _ := resp["scopes"].(string); got != "read,write" {
		t.Errorf("scopes: got %q, want read,write", got)
	}
}

// TestRotateToken_NarrowPATStillRotates pins that rotation — which re-issues
// the same binding and scopes, never a wider one — is unaffected.
func TestRotateToken_NarrowPATStillRotates(t *testing.T) {
	fixture := newSubsetFixture(t, "user")
	pat := fixture.authenticateAs("ci-read-a", "read", subsetProjectA)

	w := doAuthRequest(fixture.router, http.MethodPost,
		routeTokens+"/"+auth.HashToken(pat)+rotateSuffix, pat, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("rotate: got %d, want 201 (body: %s)", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if got, _ := resp["scopes"].(string); got != "read" {
		t.Errorf("rotated scopes: got %q, want read", got)
	}
	if got, _ := resp["projectId"].(string); got != subsetProjectA {
		t.Errorf("rotated projectId: got %q, want %q", got, subsetProjectA)
	}
}

// TestCreateToken_AdminSessionMintsServiceToken keeps the bootstrap path
// working: a platform admin acting from a session mints a capability token
// for a service principal.
func TestCreateToken_AdminSessionMintsServiceToken(t *testing.T) {
	fixture := newSubsetFixture(t, "platform_admin")
	now := time.Now()
	fixture.users.users["svc-1"] = &domain.User{
		ID: "svc-1", Username: testutil.FixtureToken("graphql"),
		Role: "platform_viewer", Active: true, Kind: domain.UserKindService, CreatedAt: &now,
	}
	session := fixture.authenticateAs("admin-login", auth.ScopeSession, "")

	w := doAuthRequest(fixture.router, http.MethodPost, routeTokens+"/", session,
		`{"name":"graphql","userId":"svc-1","permissions":["policies:read"],"scopes":["read"]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("admin session minting a service token: got %d, want 201 (body: %s)", w.Code, w.Body.String())
	}
}

// TestRotateToken_NarrowPATCannotRotateBroaderToken covers the sibling route:
// rotation hands the caller a fresh secret, so a narrow PAT rotating the
// owner's all-purpose token would be the same escalation by another door.
func TestRotateToken_NarrowPATCannotRotateBroaderToken(t *testing.T) {
	fixture := newSubsetFixture(t, "user")
	narrow := fixture.authenticateAs("ci-read-a", "read", subsetProjectA)
	broad := fixture.authenticateAs("laptop", "", "")

	w := doAuthRequest(fixture.router, http.MethodPost,
		routeTokens+"/"+auth.HashToken(broad)+rotateSuffix, narrow, "")
	if w.Code != http.StatusForbidden {
		t.Errorf("narrow PAT rotating a broader token: got %d, want 403 (body: %s)", w.Code, w.Body.String())
	}
}

// TestRotateToken_SessionRotatesAnyOwnToken keeps the studio path open.
func TestRotateToken_SessionRotatesAnyOwnToken(t *testing.T) {
	fixture := newSubsetFixture(t, "user")
	session := fixture.authenticateAs("login", auth.ScopeSession, "")
	broad := fixture.authenticateAs("laptop", "", "")

	w := doAuthRequest(fixture.router, http.MethodPost,
		routeTokens+"/"+auth.HashToken(broad)+rotateSuffix, session, "")
	if w.Code != http.StatusCreated {
		t.Errorf("session rotating an owned token: got %d, want 201 (body: %s)", w.Code, w.Body.String())
	}
}
