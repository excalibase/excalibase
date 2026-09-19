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
	instances.Create(&domain.DatabaseInstance{ProjectID: subsetProjectA, OrgID: subsetOrg})
	instances.Create(&domain.DatabaseInstance{ProjectID: subsetProjectB, OrgID: subsetOrg})
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
		{"widen scopes", "write", subsetProjectA, `{"name":"n","scopes":["read","write"]}`, http.StatusForbidden},
		{"drop the project binding", "write", subsetProjectA, `{"name":"n","scopes":["write"]}`, http.StatusForbidden},
		// A bound token cannot see another project at all, so the existing
		// visibility check answers first — and 404 is the better answer: it
		// does not confirm that project B exists.
		{"rebind to another project", "write", subsetProjectA, `{"name":"n","projectId":"` + subsetProjectB + `","scopes":["write"]}`, http.StatusNotFound},
		{"become all-purpose", "write", "", `{"name":"n"}`, http.StatusForbidden},
		// Minting is a write: a read-only PAT never reaches the subset rule.
		{"read-only caller may not mint at all", "read", subsetProjectA, `{"name":"n","projectId":"` + subsetProjectA + `","scopes":["read"]}`, http.StatusForbidden},
		{"same binding and scopes", "write", subsetProjectA, `{"name":"n","projectId":"` + subsetProjectA + `","scopes":["write"]}`, http.StatusCreated},
		{"narrower scopes", "read,write", subsetProjectA, `{"name":"n","projectId":"` + subsetProjectA + `","scopes":["read"]}`, http.StatusCreated},
		{"unbound caller binds a visible project", "write", "", `{"name":"n","projectId":"` + subsetProjectA + `","scopes":["write"]}`, http.StatusCreated},
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
	pat := fixture.authenticateAs("ci-rw-a", "read,write", subsetProjectA)

	w := doAuthRequest(fixture.router, http.MethodPost,
		routeTokens+"/"+auth.HashToken(pat)+rotateSuffix, pat, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("rotate: got %d, want 201 (body: %s)", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if got, _ := resp["scopes"].(string); got != "read,write" {
		t.Errorf("rotated scopes: got %q, want read,write", got)
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
	narrow := fixture.authenticateAs("ci-rw-a", "read,write", subsetProjectA)
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

const (
	subsetServiceID   = "subset-svc-1"
	subsetCapability  = "policies:read"
	subsetServiceBody = `{"name":"svc","userId":"` + subsetServiceID + `","permissions":["` + subsetCapability + `"],"scopes":["read"]}`
)

// addServicePrincipal registers a service account the fixture can mint
// capability tokens for.
func (f *subsetFixture) addServicePrincipal() {
	now := time.Now()
	f.users.users[subsetServiceID] = &domain.User{
		ID: subsetServiceID, Username: testutil.FixtureToken("svc"),
		Role: "platform_admin", Active: true, Kind: domain.UserKindService, CreatedAt: &now,
	}
}

// storeCapabilityToken persists a capability token owned by the service
// principal and returns its raw secret.
func (f *subsetFixture) storeCapabilityToken(name string) string {
	raw := testutil.FixtureToken(name)
	now := time.Now()
	f.tokens.tokens[auth.HashToken(raw)] = &domain.AccessToken{
		TokenHash: auth.HashToken(raw), TokenPrefix: auth.TokenPrefix(raw),
		UserID: subsetServiceID, Name: name, Scopes: auth.ScopeRead,
		Permissions: []string{subsetCapability}, CreatedAt: &now,
	}
	return raw
}

// TestCreateToken_RestrictedPATCannotMintCapabilityToken is the EXC-396
// capability half: a capability token carries platform permissions that no
// scope restriction on the minting PAT bounds, so only an unrestricted
// credential may hand one out — whatever the calling user's role says.
func TestCreateToken_RestrictedPATCannotMintCapabilityToken(t *testing.T) {
	cases := []struct {
		name          string
		callerScopes  string
		callerProject string
		want          int
	}{
		{"read-only PAT bound to a project", auth.ScopeRead, subsetProjectA, http.StatusForbidden},
		{"write PAT bound to a project", "read,write", subsetProjectA, http.StatusForbidden},
		{"unbound write PAT", "read,write", "", http.StatusForbidden},
		{"admin session", auth.ScopeSession, "", http.StatusCreated},
		{"legacy all-purpose PAT", "", "", http.StatusCreated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSubsetFixture(t, "platform_admin")
			fixture.addServicePrincipal()
			caller := fixture.authenticateAs("caller", tc.callerScopes, tc.callerProject)
			before := len(fixture.tokens.tokens)

			w := doAuthRequest(fixture.router, http.MethodPost, routeTokens+"/", caller, subsetServiceBody)
			if w.Code != tc.want {
				t.Fatalf("got %d, want %d (body: %s)", w.Code, tc.want, w.Body.String())
			}
			if tc.want == http.StatusForbidden && len(fixture.tokens.tokens) != before {
				t.Errorf("refused request persisted a token: %d stored, want %d", len(fixture.tokens.tokens), before)
			}
		})
	}
}

// TestRotateToken_RestrictedPATCannotRotateServiceToken closes the sibling
// door: rotation returns a fresh secret for the capability token, so it is a
// mint of the same authority under another name.
func TestRotateToken_RestrictedPATCannotRotateServiceToken(t *testing.T) {
	fixture := newSubsetFixture(t, "platform_admin")
	fixture.addServicePrincipal()
	service := fixture.storeCapabilityToken("svc-token")
	caller := fixture.authenticateAs("admin-ci", "read,write", "")

	w := doAuthRequest(fixture.router, http.MethodPost,
		routeTokens+"/"+auth.HashToken(service)+rotateSuffix, caller, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("restricted PAT rotating a service token: got %d, want 403 (body: %s)", w.Code, w.Body.String())
	}
	if fixture.tokens.tokens[auth.HashToken(service)] == nil {
		t.Error("refused rotation retired the old secret")
	}
}

// TestRotateToken_AdminSessionRotatesServiceToken keeps the documented
// operator path open (production-k8s-runbook §4.4).
func TestRotateToken_AdminSessionRotatesServiceToken(t *testing.T) {
	fixture := newSubsetFixture(t, "platform_admin")
	fixture.addServicePrincipal()
	service := fixture.storeCapabilityToken("svc-token")
	session := fixture.authenticateAs("admin-login", auth.ScopeSession, "")

	w := doAuthRequest(fixture.router, http.MethodPost,
		routeTokens+"/"+auth.HashToken(service)+rotateSuffix, session, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("admin session rotating a service token: got %d, want 201 (body: %s)", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	permissions, _ := resp["permissions"].([]interface{})
	if len(permissions) != 1 || permissions[0] != subsetCapability {
		t.Errorf("rotated permissions: got %v, want [%s]", resp["permissions"], subsetCapability)
	}
}

// TestRotateToken_CapabilityTokenMayOnlyRotateItself pins the handler-level
// rule. In production middleware.CapabilityGate refuses every non-GET a
// capability token makes, so neither door is reachable at all (see
// TestCapabilityTokenCannotRotateItself); this keeps the rule true if the
// gate ever widens.
func TestRotateToken_CapabilityTokenMayOnlyRotateItself(t *testing.T) {
	fixture := newSubsetFixture(t, "platform_admin")
	fixture.addServicePrincipal()
	self := fixture.storeCapabilityToken("svc-self")
	other := fixture.storeCapabilityToken("svc-other")

	w := doAuthRequest(fixture.router, http.MethodPost,
		routeTokens+"/"+auth.HashToken(other)+rotateSuffix, self, "")
	if w.Code != http.StatusForbidden {
		t.Errorf("capability token rotating a sibling: got %d, want 403 (body: %s)", w.Code, w.Body.String())
	}
}
