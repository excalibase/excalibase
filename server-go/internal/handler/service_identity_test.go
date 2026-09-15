package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/go-chi/chi/v5"
)

const (
	routeServiceAccounts = "/api/admin/service-accounts"
	svcAuthName          = "svc-auth"
	svcGraphqlName       = "svc-graphql"
	adminUserID          = "admin-1"
	humanUserID          = "human-1"
	permVaultSigning     = "vault:read:pki/signing/*"
	permProjectInfo      = "projects:info:read"
	permPolicies         = "policies:read"
	bodyFmt              = "status %d body %s"
)

// identityFixture wires the auth + service-account handlers onto one router
// behind the same middleware chain the server mounts, so a test exercises the
// real gate rather than a handler in isolation.
type identityFixture struct {
	r     chi.Router
	us    *mockUserStore
	ts    *mockTokenStore
	admin string
	human string
}

func newIdentityFixture(t *testing.T) *identityFixture {
	t.Helper()
	us, ts := newMockUserStore(), newMockTokenStore()
	now := time.Now()
	us.users[adminUserID] = &domain.User{ID: adminUserID, Username: testutil.FixtureToken("root"), Role: "platform_admin", Active: true, Kind: domain.UserKindHuman, CreatedAt: &now}
	us.users[humanUserID] = &domain.User{ID: humanUserID, Username: testutil.FixtureToken("dev"), Role: "user", Active: true, Kind: domain.UserKindHuman, CreatedAt: &now}

	authHandler := NewAuthHandler(us, ts)
	svcHandler := NewServiceAccountHandler(us, ts, nil)

	r := chi.NewRouter()
	r.Use(auth.ExtractAuth(&storeLookup{us: us, ts: ts}))
	r.Use(custommw.CapabilityGate)
	r.Route("/api/auth", func(r chi.Router) {
		r.With(auth.RequireAuth).Get("/me", authHandler.Me)
		r.With(auth.RequireAuth).Post("/login", authHandler.Login)
		r.With(auth.RequireAuth).Route(routeTokens, func(r chi.Router) {
			r.Post("/", authHandler.CreateToken)
			r.Post("/{tokenHash}"+rotateSuffix, authHandler.RotateToken)
		})
	})
	r.Route("/api/admin/service-accounts", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		svcHandler.Routes(r)
	})

	f := &identityFixture{r: r, us: us, ts: ts}
	f.admin = f.mintPAT("admin-pat", adminUserID)
	f.human = f.mintPAT("human-pat", humanUserID)
	return f
}

// mintPAT stores an ordinary, permission-less PAT and returns the raw secret.
func (f *identityFixture) mintPAT(name, userID string) string {
	raw := testutil.FixtureToken(name)
	now := time.Now()
	f.ts.tokens[auth.HashToken(raw)] = &domain.AccessToken{
		TokenHash: auth.HashToken(raw), TokenPrefix: auth.TokenPrefix(raw),
		UserID: userID, Name: name, CreatedAt: &now,
	}
	return raw
}

func (f *identityFixture) do(method, path, bearer, body string) *httptest.ResponseRecorder {
	return doAuthRequest(f.r, method, path, bearer, body)
}

// createServiceAccount registers a principal as the platform admin.
func (f *identityFixture) createServiceAccount(t *testing.T, name string) *domain.User {
	t.Helper()
	w := f.do(http.MethodPost, routeServiceAccounts, f.admin, `{"name":"`+name+`"}`)
	if w.Code != http.StatusCreated && w.Code != http.StatusOK {
		t.Fatalf(bodyFmt, w.Code, w.Body.String())
	}
	var user domain.User
	if err := json.NewDecoder(strings.NewReader(w.Body.String())).Decode(&user); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return &user
}

// mintCapability mints a capability token for name with the given permissions
// and returns the raw secret.
func (f *identityFixture) mintCapability(t *testing.T, name string, permissions []string) string {
	t.Helper()
	user := f.createServiceAccount(t, name)
	perms, _ := json.Marshal(permissions)
	body := `{"name":"` + name + `","expiresIn":"never","userId":"` + user.ID + `","permissions":` + string(perms) + `}`
	w := f.do(http.MethodPost, routeAuthTokens, f.admin, body)
	if w.Code != http.StatusCreated {
		t.Fatalf(bodyFmt, w.Code, w.Body.String())
	}
	return decodeBody(t, w.Body.String())["token"].(string)
}

// --- service principals ---

func TestServiceAccountCreateIsIdempotent(t *testing.T) {
	f := newIdentityFixture(t)
	first := f.createServiceAccount(t, svcAuthName)
	if first.Kind != domain.UserKindService {
		t.Fatalf("kind = %q, want service", first.Kind)
	}
	if first.PasswordHash != "" {
		t.Fatal("a service principal must have no password")
	}
	again := f.do(http.MethodPost, routeServiceAccounts, f.admin, `{"name":"`+svcAuthName+`"}`)
	if again.Code != http.StatusOK {
		t.Fatalf("re-create: "+bodyFmt, again.Code, again.Body.String())
	}
	second := f.createServiceAccount(t, svcAuthName)
	if second.ID != first.ID {
		t.Fatalf("re-create minted a second principal: %s vs %s", second.ID, first.ID)
	}
}

func TestServiceAccountRoutesAreAdminOnly(t *testing.T) {
	f := newIdentityFixture(t)
	for _, call := range []struct{ method, path, body string }{
		{http.MethodPost, routeServiceAccounts, `{"name":"svc-x"}`},
		{http.MethodGet, routeServiceAccounts, ""},
		{http.MethodDelete, routeServiceAccounts + "/" + svcAuthName, ""},
	} {
		w := f.do(call.method, call.path, f.human, call.body)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s as a normal user: "+bodyFmt, call.method, call.path, w.Code, w.Body.String())
		}
	}
}

func TestServiceAccountRejectsAnInvalidName(t *testing.T) {
	f := newIdentityFixture(t)
	for _, name := range []string{"", "A", "Svc-Auth", "svc auth", "svc/auth"} {
		w := f.do(http.MethodPost, routeServiceAccounts, f.admin, `{"name":"`+name+`"}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("name %q: "+bodyFmt, name, w.Code, w.Body.String())
		}
	}
}

func TestServiceAccountDeleteRevokesItsTokens(t *testing.T) {
	f := newIdentityFixture(t)
	raw := f.mintCapability(t, svcGraphqlName, []string{permPolicies})
	if code := f.do(http.MethodGet, routeAuthMe, raw, "").Code; code != http.StatusOK {
		t.Fatalf("service token should authenticate: %d", code)
	}
	if w := f.do(http.MethodDelete, routeServiceAccounts+"/"+svcGraphqlName, f.admin, ""); w.Code != http.StatusOK {
		t.Fatalf("delete: "+bodyFmt, w.Code, w.Body.String())
	}
	if code := f.do(http.MethodGet, routeAuthMe, raw, "").Code; code != http.StatusUnauthorized {
		t.Fatalf("token survived the delete: %d", code)
	}
}

func TestServiceAccountCannotLogIn(t *testing.T) {
	f := newIdentityFixture(t)
	f.createServiceAccount(t, svcAuthName)
	w := f.do(http.MethodPost, "/api/auth/login", f.admin, `{"username":"`+svcAuthName+`","password":""}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("service login: "+bodyFmt, w.Code, w.Body.String())
	}
}

// --- capability token minting ---

func TestMintCapabilityTokenRequiresPlatformAdmin(t *testing.T) {
	f := newIdentityFixture(t)
	user := f.createServiceAccount(t, svcAuthName)
	body := `{"name":"svc","userId":"` + user.ID + `","permissions":["` + permPolicies + `"]}`
	if w := f.do(http.MethodPost, routeAuthTokens, f.human, body); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin mint: "+bodyFmt, w.Code, w.Body.String())
	}
}

func TestMintCapabilityTokenValidatesTheSubject(t *testing.T) {
	f := newIdentityFixture(t)
	service := f.createServiceAccount(t, svcAuthName)
	cases := []struct{ name, body string }{
		{"permissions without a subject", `{"name":"x","permissions":["` + permPolicies + `"]}`},
		{"subject is a human", `{"name":"x","userId":"` + humanUserID + `","permissions":["` + permPolicies + `"]}`},
		{"unknown subject", `{"name":"x","userId":"nope","permissions":["` + permPolicies + `"]}`},
		{"service without permissions", `{"name":"x","userId":"` + service.ID + `"}`},
		{"malformed permission", `{"name":"x","userId":"` + service.ID + `","permissions":["nonsense"]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := f.do(http.MethodPost, routeAuthTokens, f.admin, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf(bodyFmt, w.Code, w.Body.String())
			}
		})
	}
}

func TestMintedCapabilityTokenCarriesItsPermissions(t *testing.T) {
	f := newIdentityFixture(t)
	raw := f.mintCapability(t, svcAuthName, []string{permVaultSigning, permProjectInfo})
	stored := f.ts.tokens[auth.HashToken(raw)]
	if len(stored.Permissions) != 2 {
		t.Fatalf("permissions = %v", stored.Permissions)
	}
	if stored.ExpiresAt != nil {
		t.Fatalf("expiresIn:never should not set an expiry: %v", stored.ExpiresAt)
	}
}

// --- the gate, end to end over the router ---

func TestCapabilityTokenIsConfinedToItsPermissions(t *testing.T) {
	f := newIdentityFixture(t)
	graphql := f.mintCapability(t, svcGraphqlName, []string{permProjectInfo, permPolicies})

	// /me is always reachable so a service can validate its own credential.
	if code := f.do(http.MethodGet, routeAuthMe, graphql, "").Code; code != http.StatusOK {
		t.Fatalf("/me = %d, want 200", code)
	}
	// Minting more credentials is refused even though the owner is an admin.
	w := f.do(http.MethodPost, routeAuthTokens, graphql, `{"name":"escalate"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("capability token minted a token: "+bodyFmt, w.Code, w.Body.String())
	}
	// So is the service-account surface.
	if code := f.do(http.MethodGet, routeServiceAccounts, graphql, "").Code; code != http.StatusForbidden {
		t.Fatalf("service-account listing = %d, want 403", code)
	}
}

func TestHumanPatIsUnaffectedByTheGate(t *testing.T) {
	f := newIdentityFixture(t)
	if code := f.do(http.MethodGet, routeServiceAccounts, f.admin, "").Code; code != http.StatusOK {
		t.Fatalf("admin PAT blocked by the capability gate: %d", code)
	}
}

func TestExpiredCapabilityTokenIsUnauthorized(t *testing.T) {
	f := newIdentityFixture(t)
	raw := f.mintCapability(t, svcAuthName, []string{permProjectInfo})
	past := time.Now().Add(-time.Hour)
	f.ts.tokens[auth.HashToken(raw)].ExpiresAt = &past
	if code := f.do(http.MethodGet, routeAuthMe, raw, "").Code; code != http.StatusUnauthorized {
		t.Fatalf("expired capability token = %d, want 401", code)
	}
}

func TestRevokedCapabilityTokenIsUnauthorized(t *testing.T) {
	f := newIdentityFixture(t)
	raw := f.mintCapability(t, svcAuthName, []string{permProjectInfo})
	delete(f.ts.tokens, auth.HashToken(raw))
	if code := f.do(http.MethodGet, routeAuthMe, raw, "").Code; code != http.StatusUnauthorized {
		t.Fatalf("revoked capability token = %d, want 401", code)
	}
}

// --- rotation ---

func TestRotationPreservesPermissions(t *testing.T) {
	f := newIdentityFixture(t)
	want := []string{permProjectInfo, permPolicies}
	raw := f.mintCapability(t, svcGraphqlName, want)
	oldHash := auth.HashToken(raw)

	w := f.do(http.MethodPost, routeAuthTokens+"/"+oldHash+rotateSuffix, f.admin, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("admin rotation of a service token: "+bodyFmt, w.Code, w.Body.String())
	}
	fresh := decodeBody(t, w.Body.String())["token"].(string)
	rotated := f.ts.tokens[auth.HashToken(fresh)]
	if len(rotated.Permissions) != len(want) {
		t.Fatalf("permissions after rotation = %v, want %v", rotated.Permissions, want)
	}
	if rotated.ExpiresAt != nil {
		t.Fatalf("rotation must keep the never-expires lifetime, got %v", rotated.ExpiresAt)
	}
	if _, alive := f.ts.tokens[oldHash]; alive {
		t.Fatal("the old secret was not retired")
	}
	if code := f.do(http.MethodGet, routeAuthMe, fresh, "").Code; code != http.StatusOK {
		t.Fatalf("rotated token = %d, want 200", code)
	}
}

func TestAdminMayNotRotateAHumanToken(t *testing.T) {
	f := newIdentityFixture(t)
	hash := auth.HashToken(f.human)
	w := f.do(http.MethodPost, routeAuthTokens+"/"+hash+rotateSuffix, f.admin, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("admin rotated another human's PAT: "+bodyFmt, w.Code, w.Body.String())
	}
}
