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
	audit *mockAudit
	admin string
	human string
}

func newIdentityFixture(t *testing.T) *identityFixture {
	t.Helper()
	us, ts := newMockUserStore(), newMockTokenStore()
	now := time.Now()
	us.users[adminUserID] = &domain.User{ID: adminUserID, Username: testutil.FixtureToken("root"), Role: "platform_admin", Active: true, Kind: domain.UserKindHuman, CreatedAt: &now}
	us.users[humanUserID] = &domain.User{ID: humanUserID, Username: testutil.FixtureToken("dev"), Role: "user", Active: true, Kind: domain.UserKindHuman, CreatedAt: &now}

	audit := &mockAudit{}
	authHandler := NewAuthHandler(us, ts)
	svcHandler := NewServiceAccountHandler(us, ts, audit)

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

	f := &identityFixture{r: r, us: us, ts: ts, audit: audit}
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

// --- service-account surface: refusals and store failures ---

func TestServiceAccountCreateRejectsAMalformedBody(t *testing.T) {
	f := newIdentityFixture(t)
	w := f.do(http.MethodPost, routeServiceAccounts, f.admin, `{"name":`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf(bodyFmt, w.Code, w.Body.String())
	}
}

func TestServiceAccountCreateRefusesAHumanName(t *testing.T) {
	f := newIdentityFixture(t)
	human := f.us.users[humanUserID]
	w := f.do(http.MethodPost, routeServiceAccounts, f.admin, `{"name":"`+human.Username+`"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("taking over a human name: "+bodyFmt, w.Code, w.Body.String())
	}
	// The human account must be untouched — no kind flip, no new principal.
	if human.IsService() {
		t.Fatal("the human account was converted into a service principal")
	}
}

func TestServiceAccountCreateSurfacesAStoreFailure(t *testing.T) {
	f := newIdentityFixture(t)
	f.us.failSave = true
	w := f.do(http.MethodPost, routeServiceAccounts, f.admin, `{"name":"`+svcAuthName+`"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf(bodyFmt, w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "db error") {
		t.Fatalf("the store error leaked to the client: %s", w.Body.String())
	}
}

func TestServiceAccountListOnlyIncludesServicePrincipals(t *testing.T) {
	f := newIdentityFixture(t)
	f.createServiceAccount(t, svcAuthName)
	f.createServiceAccount(t, svcGraphqlName)

	w := f.do(http.MethodGet, routeServiceAccounts, f.admin, "")
	if w.Code != http.StatusOK {
		t.Fatalf(bodyFmt, w.Code, w.Body.String())
	}
	var listed []domain.User
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed %d accounts, want the 2 service principals: %s", len(listed), w.Body.String())
	}
	for _, u := range listed {
		if !u.IsService() {
			t.Fatalf("a human account appeared in the service listing: %s", u.Username)
		}
	}
}

func TestServiceAccountListSurfacesAStoreFailure(t *testing.T) {
	f := newIdentityFixture(t)
	f.us.failList = true
	w := f.do(http.MethodGet, routeServiceAccounts, f.admin, "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf(bodyFmt, w.Code, w.Body.String())
	}
}

func TestServiceAccountTokenListingShowsMetadataOnly(t *testing.T) {
	f := newIdentityFixture(t)
	raw := f.mintCapability(t, svcGraphqlName, []string{permPolicies, permProjectInfo})

	w := f.do(http.MethodGet, routeServiceAccounts+"/"+svcGraphqlName+"/tokens", f.admin, "")
	if w.Code != http.StatusOK {
		t.Fatalf(bodyFmt, w.Code, w.Body.String())
	}
	var views []serviceTokenView
	if err := json.Unmarshal(w.Body.Bytes(), &views); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("listed %d tokens, want 1: %s", len(views), w.Body.String())
	}
	view := views[0]
	if view.TokenHash != auth.HashToken(raw) {
		t.Fatalf("tokenHash = %q, want the stored digest", view.TokenHash)
	}
	if view.TokenPrefix != auth.TokenPrefix(raw) {
		t.Fatalf("tokenPrefix = %q, want %q", view.TokenPrefix, auth.TokenPrefix(raw))
	}
	if len(view.Permissions) != 2 {
		t.Fatalf("permissions = %v, want both", view.Permissions)
	}
	// The raw secret is returned once at minting and never again.
	if strings.Contains(w.Body.String(), raw) {
		t.Fatal("the token listing leaked the raw secret")
	}
}

func TestServiceAccountTokenListingRefusesNonServiceNames(t *testing.T) {
	f := newIdentityFixture(t)
	human := f.us.users[humanUserID]
	for _, name := range []string{"does-not-exist", human.Username} {
		w := f.do(http.MethodGet, routeServiceAccounts+"/"+name+"/tokens", f.admin, "")
		if w.Code != http.StatusNotFound {
			t.Fatalf("name %q: "+bodyFmt, name, w.Code, w.Body.String())
		}
	}
}

func TestServiceAccountTokenListingSurfacesAStoreFailure(t *testing.T) {
	f := newIdentityFixture(t)
	f.createServiceAccount(t, svcAuthName)
	f.ts.failList = true
	w := f.do(http.MethodGet, routeServiceAccounts+"/"+svcAuthName+"/tokens", f.admin, "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf(bodyFmt, w.Code, w.Body.String())
	}
}

// TestServiceAccountDeleteFailsLoudly pins the ordering guarantee that matters:
// when any step of the revoke-then-delete sequence fails the caller is told,
// and the principal is not left deleted with its credentials still live.
func TestServiceAccountDeleteFailsLoudly(t *testing.T) {
	cases := []struct {
		name     string
		sabotage func(f *identityFixture)
	}{
		{name: "token listing fails", sabotage: func(f *identityFixture) { f.ts.failList = true }},
		{name: "token revocation fails", sabotage: func(f *identityFixture) { f.ts.failDelete = true }},
		{name: "principal delete fails", sabotage: func(f *identityFixture) { f.us.failDelete = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newIdentityFixture(t)
			service := f.createServiceAccount(t, svcGraphqlName)
			f.mintPAT("svc-token", service.ID)
			tc.sabotage(f)

			w := f.do(http.MethodDelete, routeServiceAccounts+"/"+svcGraphqlName, f.admin, "")
			if w.Code != http.StatusInternalServerError {
				t.Fatalf(bodyFmt, w.Code, w.Body.String())
			}
			if _, alive := f.us.users[service.ID]; !alive {
				t.Fatal("the principal was deleted despite the reported failure")
			}
		})
	}
}

func TestServiceAccountDeleteRefusesAHumanAccount(t *testing.T) {
	f := newIdentityFixture(t)
	human := f.us.users[humanUserID]
	w := f.do(http.MethodDelete, routeServiceAccounts+"/"+human.Username, f.admin, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("deleting a human through the service surface: "+bodyFmt, w.Code, w.Body.String())
	}
	if _, alive := f.us.users[humanUserID]; !alive {
		t.Fatal("the human account was deleted")
	}
}

func TestServiceAccountChangesAreAudited(t *testing.T) {
	f := newIdentityFixture(t)
	service := f.createServiceAccount(t, svcAuthName)
	if w := f.do(http.MethodDelete, routeServiceAccounts+"/"+svcAuthName, f.admin, ""); w.Code != http.StatusOK {
		t.Fatalf("delete: "+bodyFmt, w.Code, w.Body.String())
	}
	if len(f.audit.entries) != 2 {
		t.Fatalf("audit entries = %d, want create + delete", len(f.audit.entries))
	}
	for i, wantAction := range []string{"service_account.create", "service_account.delete"} {
		entry := f.audit.entries[i]
		if entry.Action != wantAction {
			t.Fatalf("entry %d action = %q, want %q", i, entry.Action, wantAction)
		}
		if entry.ResourceID != service.ID {
			t.Fatalf("entry %d resourceId = %q, want %q", i, entry.ResourceID, service.ID)
		}
		if entry.UserID != adminUserID {
			t.Fatalf("entry %d actor = %q, want the calling admin", i, entry.UserID)
		}
	}
}

// --- unit-level refusals ---

func TestResolveTokenSubjectRefusals(t *testing.T) {
	us, ts := newMockUserStore(), newMockTokenStore()
	now := time.Now()
	admin := &domain.User{ID: adminUserID, Role: "platform_admin", Active: true, Kind: domain.UserKindHuman, CreatedAt: &now}
	human := &domain.User{ID: humanUserID, Role: "user", Active: true, Kind: domain.UserKindHuman, CreatedAt: &now}
	service := &domain.User{ID: "svc-1", Role: "platform_admin", Active: true, Kind: domain.UserKindService, CreatedAt: &now}
	us.users[admin.ID], us.users[human.ID], us.users[service.ID] = admin, human, service
	h := NewAuthHandler(us, ts)
	req := httptest.NewRequest(http.MethodPost, routeAuthTokens, nil)

	cases := []struct {
		name    string
		caller  *domain.User
		req     createTokenRequest
		status  int
		message string
	}{
		{name: "non-admin naming a subject", caller: human,
			req:    createTokenRequest{UserID: service.ID, Permissions: []string{permPolicies}},
			status: http.StatusForbidden, message: "only a platform admin may mint a token for a service account"},
		{name: "permissions without a subject", caller: admin,
			req:    createTokenRequest{Permissions: []string{permPolicies}},
			status: http.StatusBadRequest, message: "permissions require userId naming a service account"},
		{name: "subject is a human", caller: admin,
			req:    createTokenRequest{UserID: human.ID, Permissions: []string{permPolicies}},
			status: http.StatusBadRequest, message: "userId must name a service account"},
		{name: "service without permissions", caller: admin,
			req:    createTokenRequest{UserID: service.ID},
			status: http.StatusBadRequest, message: "a service account token requires at least one permission"},
		{name: "empty permission list", caller: admin,
			req:    createTokenRequest{UserID: service.ID, Permissions: []string{}},
			status: http.StatusBadRequest, message: "a service account token requires at least one permission"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var spec tokenSpec
			err := h.resolveTokenSubject(req, tc.caller, tc.req, &spec)
			if err == nil {
				t.Fatalf("expected a refusal, spec = %+v", spec)
			}
			if err.status != tc.status {
				t.Fatalf("status = %d, want %d", err.status, tc.status)
			}
			if err.Error() != tc.message {
				t.Fatalf("message = %q, want %q", err.Error(), tc.message)
			}
			if spec.userID != "" || spec.permissions != nil {
				t.Fatalf("a refused request still mutated the spec: %+v", spec)
			}
		})
	}
}

func TestResolveTokenSubjectAcceptsAServicePrincipal(t *testing.T) {
	us, ts := newMockUserStore(), newMockTokenStore()
	now := time.Now()
	admin := &domain.User{ID: adminUserID, Role: "platform_admin", Active: true, Kind: domain.UserKindHuman, CreatedAt: &now}
	service := &domain.User{ID: "svc-1", Role: "platform_admin", Active: true, Kind: domain.UserKindService, CreatedAt: &now}
	us.users[admin.ID], us.users[service.ID] = admin, service
	h := NewAuthHandler(us, ts)
	req := httptest.NewRequest(http.MethodPost, routeAuthTokens, nil)

	spec := tokenSpec{userID: admin.ID}
	// The duplicate is normalized away, so the stored list is the canonical one.
	body := createTokenRequest{UserID: service.ID, Permissions: []string{permPolicies, permPolicies, permProjectInfo}}
	if err := h.resolveTokenSubject(req, admin, body, &spec); err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if spec.userID != service.ID {
		t.Fatalf("spec.userID = %q, want the service principal", spec.userID)
	}
	if len(spec.permissions) != 2 {
		t.Fatalf("permissions = %v, want the two distinct entries", spec.permissions)
	}
}

// TestResolveTokenSubjectLeavesAPlainPatAlone pins that the service rules are
// additive: a self-minted PAT names no subject and carries no permissions, so
// it keeps the caller's own id and stays an ordinary token.
func TestResolveTokenSubjectLeavesAPlainPatAlone(t *testing.T) {
	us, ts := newMockUserStore(), newMockTokenStore()
	human := &domain.User{ID: humanUserID, Role: "user", Active: true, Kind: domain.UserKindHuman}
	us.users[human.ID] = human
	h := NewAuthHandler(us, ts)
	req := httptest.NewRequest(http.MethodPost, routeAuthTokens, nil)

	spec := tokenSpec{userID: human.ID}
	if err := h.resolveTokenSubject(req, human, createTokenRequest{Name: "laptop"}, &spec); err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if spec.userID != human.ID || spec.permissions != nil {
		t.Fatalf("a plain PAT was altered: %+v", spec)
	}
}

// TestOwnedByServiceWithoutAUserStore pins the fail-closed default: with no
// user store to ask, no token counts as service-owned, so an admin cannot
// rotate a human's PAT through the service-rotation allowance.
func TestOwnedByServiceWithoutAUserStore(t *testing.T) {
	h := NewAuthHandler(nil, newMockTokenStore())
	if h.ownedByService(t.Context(), &domain.AccessToken{UserID: humanUserID}) {
		t.Fatal("ownedByService must be false when there is no user store")
	}
}

// TestResolveInviteeByIdEmailOrNeither pins the lookup order an org invite
// uses: an explicit userId wins, an email is the fallback, and naming neither
// resolves to nobody rather than to an arbitrary account.
func TestResolveInviteeByIdEmailOrNeither(t *testing.T) {
	us := newMockUserStore()
	byID := &domain.User{ID: "u-id", Username: "by-id", Email: "id@test.local", Kind: domain.UserKindHuman}
	byEmail := &domain.User{ID: "u-email", Username: "by-email", Email: "email@test.local", Kind: domain.UserKindHuman}
	us.users[byID.ID], us.users[byEmail.ID] = byID, byEmail
	h := &OrgHandler{userStore: us}

	cases := []struct {
		name, userID, email, want string
	}{
		{name: "by id", userID: byID.ID, want: byID.ID},
		{name: "id wins over email", userID: byID.ID, email: byEmail.Email, want: byID.ID},
		{name: "by email", email: byEmail.Email, want: byEmail.ID},
		{name: "neither", want: ""},
		{name: "unknown email", email: "nobody@test.local", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			user, err := h.resolveInvitee(t.Context(), tc.userID, tc.email)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := ""
			if user != nil {
				got = user.ID
			}
			if got != tc.want {
				t.Fatalf("resolved %q, want %q", got, tc.want)
			}
		})
	}
}

// TestServiceAccountAuditSinkIsOptional pins the constructor's contract: a
// platform running without an audit sink still manages service principals
// rather than failing on the write path.
func TestServiceAccountAuditSinkIsOptional(t *testing.T) {
	f := newIdentityFixture(t)
	bare := NewServiceAccountHandler(f.us, f.ts, nil)
	r := chi.NewRouter()
	r.Use(auth.ExtractAuth(&storeLookup{us: f.us, ts: f.ts}))
	r.Route(routeServiceAccounts, func(r chi.Router) {
		r.Use(auth.RequireAuth)
		bare.Routes(r)
	})

	created := doAuthRequest(r, http.MethodPost, routeServiceAccounts, f.admin, `{"name":"`+svcAuthName+`"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: "+bodyFmt, created.Code, created.Body.String())
	}
	deleted := doAuthRequest(r, http.MethodDelete, routeServiceAccounts+"/"+svcAuthName, f.admin, "")
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete: "+bodyFmt, deleted.Code, deleted.Body.String())
	}
	if len(f.audit.entries) != 0 {
		t.Fatalf("a handler with no sink wrote %d audit entries", len(f.audit.entries))
	}
}

// --- EXC-396: only an unrestricted credential may hand out service authority ---

// mintScopedPAT stores a PAT narrowed to the given scopes and returns the raw
// secret. The owner is the platform admin, so only the credential — never the
// role — decides the outcome.
func (f *identityFixture) mintScopedPAT(name, scopes string) string {
	raw := testutil.FixtureToken(name)
	now := time.Now()
	f.ts.tokens[auth.HashToken(raw)] = &domain.AccessToken{
		TokenHash: auth.HashToken(raw), TokenPrefix: auth.TokenPrefix(raw),
		UserID: adminUserID, Name: name, Scopes: scopes, CreatedAt: &now,
	}
	return raw
}

// TestServiceAccountLifecycleNeedsAnUnrestrictedCredential: registering or
// deleting a service principal decides which machine identities exist, so a
// narrowed PAT must not reach it however wide its owner's role is.
func TestServiceAccountLifecycleNeedsAnUnrestrictedCredential(t *testing.T) {
	cases := []struct {
		name, method, path string
		want               int
	}{
		{"create", http.MethodPost, routeServiceAccounts, http.StatusForbidden},
		{"delete", http.MethodDelete, routeServiceAccounts + "/" + svcAuthName, http.StatusForbidden},
		{"list stays readable", http.MethodGet, routeServiceAccounts, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newIdentityFixture(t)
			f.createServiceAccount(t, svcAuthName)
			scoped := f.mintScopedPAT("admin-ci", "read,write")

			w := f.do(tc.method, tc.path, scoped, `{"name":"`+svcGraphqlName+`"}`)
			if w.Code != tc.want {
				t.Fatalf("%s as a scoped PAT: "+bodyFmt, tc.method, w.Code, w.Body.String())
			}
		})
	}
}

// TestCapabilityTokenCannotRotateItself records what the gate actually does:
// CapabilityGate allows a capability token only the GET routes its permission
// list names, so rotation — a POST — never reaches the handler. Nothing in
// the platform self-rotates; the runbook rotates service tokens with an
// operator credential (§4.4).
func TestCapabilityTokenCannotRotateItself(t *testing.T) {
	f := newIdentityFixture(t)
	raw := f.mintCapability(t, svcGraphqlName, []string{permPolicies})

	w := f.do(http.MethodPost, routeAuthTokens+"/"+auth.HashToken(raw)+rotateSuffix, raw, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("capability token rotating itself: "+bodyFmt, w.Code, w.Body.String())
	}
	if f.ts.tokens[auth.HashToken(raw)] == nil {
		t.Fatal("a refused rotation retired the old secret")
	}
}
