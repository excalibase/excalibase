package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/go-chi/chi/v5"
)

const (
	routeAuthMe       = "/api/auth/me"
	rotateSuffix      = "/rotate"
	tokenUser         = "u1"
	tokenOtherUser    = "u2"
	statusFmt         = "status: got %d body %s"
	expiresWithinSlop = 5 * time.Second
	scopesRead        = "read"
	nameCaller        = "caller"
	nameCI            = "ci"
)

// storeLookup adapts the in-memory mocks to auth.TokenLookup so the
// ExtractAuth middleware sees tokens minted by the handler under test.
type storeLookup struct {
	us *mockUserStore
	ts *mockTokenStore
}

func (l *storeLookup) FindByTokenHash(ctx context.Context, hash string) (*domain.AccessToken, error) {
	return l.ts.FindByTokenHash(ctx, hash)
}

func (l *storeLookup) FindUserByID(ctx context.Context, id string) (*domain.User, error) {
	return l.us.FindUserByID(ctx, id)
}

type mockAudit struct{ entries []*domain.AuditEntry }

func (m *mockAudit) LogAudit(_ context.Context, e *domain.AuditEntry) error {
	m.entries = append(m.entries, e)
	return nil
}

// tokenFixture wires a router whose auth middleware reads from the same
// mock stores the handler writes to. Returns the router, the audit sink and
// a live PAT for user u1 whose hash is also the store key.
type tokenFixture struct {
	r     chi.Router
	us    *mockUserStore
	ts    *mockTokenStore
	audit *mockAudit
}

func newTokenFixture(t *testing.T) *tokenFixture {
	t.Helper()
	us, ts, audit := newMockUserStore(), newMockTokenStore(), &mockAudit{}
	now := time.Now()
	us.users[tokenUser] = &domain.User{ID: tokenUser, Username: testutil.FixtureToken("alice"), Role: "user", Active: true, CreatedAt: &now}
	us.users[tokenOtherUser] = &domain.User{ID: tokenOtherUser, Username: testutil.FixtureToken("bob"), Role: "user", Active: true, CreatedAt: &now}

	h := NewAuthHandler(us, ts)
	h.SetAuditLog(audit)
	r := chi.NewRouter()
	r.Use(auth.ExtractAuth(&storeLookup{us: us, ts: ts}))
	r.Route("/api/auth", func(r chi.Router) {
		r.Get("/me", h.Me)
		r.With(auth.RequireAuth).Route(routeTokens, func(r chi.Router) {
			r.Get("/", h.ListTokens)
			r.Post("/", h.CreateToken)
			r.Delete("/{tokenHash}", h.RevokeToken)
			r.Post("/{tokenHash}"+rotateSuffix, h.RotateToken)
		})
	})
	return &tokenFixture{r: r, us: us, ts: ts, audit: audit}
}

// mint stores a PAT for userID and returns the raw secret.
func (f *tokenFixture) mint(name, userID, scopes string, expiresAt *time.Time) string {
	raw := testutil.FixtureToken(name)
	now := time.Now()
	f.ts.tokens[auth.HashToken(raw)] = &domain.AccessToken{
		TokenHash: auth.HashToken(raw), TokenPrefix: auth.TokenPrefix(raw), UserID: userID,
		Name: name, Scopes: scopes, CreatedAt: &now, ExpiresAt: expiresAt,
	}
	return raw
}

func (f *tokenFixture) meStatus(raw string) int {
	return doAuthRequest(f.r, "GET", routeAuthMe, raw, "").Code
}

func decodeBody(t *testing.T, body string) map[string]interface{} {
	t.Helper()
	var resp map[string]interface{}
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func assertExpiresNear(t *testing.T, got interface{}, want time.Time) {
	t.Helper()
	s, ok := got.(string)
	if !ok {
		t.Fatalf("expiresAt missing or not a string: %v", got)
	}
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("expiresAt parse: %v", err)
	}
	if diff := parsed.Sub(want); diff > expiresWithinSlop || diff < -expiresWithinSlop {
		t.Errorf("expiresAt: got %v want ~%v", parsed, want)
	}
}

// --- create with expiry ---

func TestCreateToken_DefaultsTo90Days(t *testing.T) {
	f := newTokenFixture(t)
	caller := f.mint(nameCaller, tokenUser, "", nil)
	w := doAuthRequest(f.r, "POST", routeAuthTokens, caller, `{"name":"ci"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf(statusFmt, w.Code, w.Body.String())
	}
	assertExpiresNear(t, decodeBody(t, w.Body.String())["expiresAt"], time.Now().Add(auth.DefaultPATLifetime))
}

func TestCreateToken_ExplicitExpiresIn(t *testing.T) {
	f := newTokenFixture(t)
	caller := f.mint(nameCaller, tokenUser, "", nil)
	w := doAuthRequest(f.r, "POST", routeAuthTokens, caller, `{"name":"ci","expiresIn":"30d"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf(statusFmt, w.Code, w.Body.String())
	}
	assertExpiresNear(t, decodeBody(t, w.Body.String())["expiresAt"], time.Now().Add(30*24*time.Hour))
}

func TestCreateToken_NeverExpires(t *testing.T) {
	f := newTokenFixture(t)
	caller := f.mint(nameCaller, tokenUser, "", nil)
	w := doAuthRequest(f.r, "POST", routeAuthTokens, caller, `{"name":"bootstrap","expiresIn":"never"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf(statusFmt, w.Code, w.Body.String())
	}
	resp := decodeBody(t, w.Body.String())
	if resp["expiresAt"] != nil {
		t.Errorf("never should serialize a null expiresAt, got %v", resp["expiresAt"])
	}
	stored := f.ts.tokens[auth.HashToken(resp["token"].(string))]
	if stored == nil || stored.ExpiresAt != nil {
		t.Errorf("stored token must have nil expiry: %+v", stored)
	}
}

func TestCreateToken_RejectsOverMax(t *testing.T) {
	f := newTokenFixture(t)
	caller := f.mint(nameCaller, tokenUser, "", nil)
	w := doAuthRequest(f.r, "POST", routeAuthTokens, caller, `{"name":"ci","expiresIn":"400d"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf(statusFmt, w.Code, w.Body.String())
	}
}

func TestCreateToken_ExpiredTokenIs401WithCode(t *testing.T) {
	f := newTokenFixture(t)
	past := time.Now().Add(-time.Minute)
	stale := f.mint("stale", tokenUser, "", &past)
	w := doAuthRequest(f.r, "POST", routeAuthTokens, stale, `{"name":"ci"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf(statusFmt, w.Code, w.Body.String())
	}
	if code := decodeBody(t, w.Body.String())["code"]; code != auth.ErrCodeTokenExpired {
		t.Errorf("code: got %v", code)
	}
}

// --- list ---

func TestListTokens_ShowsExpiresAt(t *testing.T) {
	f := newTokenFixture(t)
	expires := time.Now().Add(time.Hour).UTC()
	caller := f.mint(nameCaller, tokenUser, "", &expires)
	w := doAuthRequest(f.r, "GET", routeAuthTokens, caller, "")
	if w.Code != http.StatusOK {
		t.Fatalf(statusFmt, w.Code, w.Body.String())
	}
	var tokens []map[string]interface{}
	json.NewDecoder(w.Body).Decode(&tokens)
	if len(tokens) != 1 {
		t.Fatalf("expected 1 token, got %d", len(tokens))
	}
	assertExpiresNear(t, tokens[0]["expiresAt"], expires)
	if _, leaked := tokens[0]["tokenHash"]; leaked {
		t.Error("list must not expose tokenHash")
	}
}

// --- rotate ---

func rotate(f *tokenFixture, caller, target, body string) (int, map[string]interface{}) {
	w := doAuthRequest(f.r, "POST", routeAuthTokens+"/"+auth.HashToken(target)+rotateSuffix, caller, body)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	return w.Code, resp
}

const rotateProject = "rotate-proj"

func TestRotateToken_ImmediateRevokePreservesBinding(t *testing.T) {
	f := newTokenFixture(t)
	expires := time.Now().Add(30 * 24 * time.Hour)
	old := f.mint(nameCI, tokenUser, scopesRead, &expires)
	f.ts.tokens[auth.HashToken(old)].ProjectID = rotateProject

	code, resp := rotate(f, old, old, `{}`)
	if code != http.StatusCreated {
		t.Fatalf("rotate: got %d %v", code, resp)
	}
	fresh, _ := resp["token"].(string)
	if fresh == "" || fresh == old {
		t.Fatal("rotation must return a new secret")
	}
	if f.meStatus(old) != http.StatusUnauthorized {
		t.Error("old token must be invalid immediately with graceSeconds=0")
	}
	if f.meStatus(fresh) != http.StatusOK {
		t.Error("new token must authenticate")
	}
	stored := f.ts.tokens[auth.HashToken(fresh)]
	if stored.Scopes != scopesRead || stored.Name != nameCI || stored.UserID != tokenUser || stored.ProjectID != rotateProject {
		t.Errorf("binding not preserved: %+v", stored)
	}
	if got, _ := resp["projectId"].(string); got != rotateProject {
		t.Errorf("response projectId %q want %q", got, rotateProject)
	}
	assertExpiresNear(t, resp["expiresAt"], time.Now().Add(30*24*time.Hour))
	if resp["previousExpiresAt"] != nil {
		t.Errorf("previousExpiresAt must be null when revoked immediately, got %v", resp["previousExpiresAt"])
	}
}

func TestRotateToken_GraceKeepsOldAlive(t *testing.T) {
	f := newTokenFixture(t)
	old := f.mint(nameCI, tokenUser, "", nil)

	code, resp := rotate(f, old, old, `{"graceSeconds":300}`)
	if code != http.StatusCreated {
		t.Fatalf("rotate: got %d %v", code, resp)
	}
	if f.meStatus(old) != http.StatusOK {
		t.Error("old token must stay valid inside the grace window")
	}
	assertExpiresNear(t, resp["previousExpiresAt"], time.Now().Add(300*time.Second))
	if resp["expiresAt"] != nil {
		t.Errorf("never-expiring token must rotate to never, got %v", resp["expiresAt"])
	}
}

func TestRotateToken_GraceNeverExtendsOldExpiry(t *testing.T) {
	f := newTokenFixture(t)
	soon := time.Now().Add(time.Minute)
	old := f.mint(nameCI, tokenUser, "", &soon)

	code, resp := rotate(f, old, old, `{"graceSeconds":3600}`)
	if code != http.StatusCreated {
		t.Fatalf("rotate: got %d %v", code, resp)
	}
	assertExpiresNear(t, resp["previousExpiresAt"], soon)
}

func TestRotateToken_RejectsGraceOverMax(t *testing.T) {
	f := newTokenFixture(t)
	old := f.mint(nameCI, tokenUser, "", nil)
	if code, _ := rotate(f, old, old, `{"graceSeconds":3601}`); code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", code)
	}
	if code, _ := rotate(f, old, old, `{"graceSeconds":-1}`); code != http.StatusBadRequest {
		t.Errorf("expected 400 for negative, got %d", code)
	}
}

func TestRotateToken_OtherUsersTokenForbidden(t *testing.T) {
	f := newTokenFixture(t)
	caller := f.mint(nameCaller, tokenUser, "", nil)
	victim := f.mint("victim", tokenOtherUser, "", nil)
	if code, _ := rotate(f, caller, victim, `{}`); code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", code)
	}
	if f.meStatus(victim) != http.StatusOK {
		t.Error("victim token must be untouched")
	}
}

func TestRotateToken_UnknownIs404(t *testing.T) {
	f := newTokenFixture(t)
	caller := f.mint(nameCaller, tokenUser, "", nil)
	if code, _ := rotate(f, caller, "no-such-token", `{}`); code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", code)
	}
}

func TestRotateToken_WritesAuditWithoutSecret(t *testing.T) {
	f := newTokenFixture(t)
	old := f.mint(nameCI, tokenUser, "", nil)
	code, resp := rotate(f, old, old, `{"graceSeconds":60}`)
	if code != http.StatusCreated {
		t.Fatalf("rotate: got %d", code)
	}
	if len(f.audit.entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(f.audit.entries))
	}
	e := f.audit.entries[0]
	if e.Action != auditActionTokenRotate || e.UserID != tokenUser || e.Resource != auditResourceToken {
		t.Errorf("audit entry: %+v", e)
	}
	fresh := resp["token"].(string)
	if strings.Contains(e.Details, fresh) || strings.Contains(e.Details, old) {
		t.Error("audit details must never contain a raw token")
	}
	if !strings.Contains(e.Details, `"graceSeconds":60`) {
		t.Errorf("audit details should record the grace window: %s", e.Details)
	}
}

// --- registration auto-login token ---

func TestRegister_AutoLoginTokenIsBoundedSession(t *testing.T) {
	us, ts := newMockUserStore(), newMockTokenStore()
	h := NewAuthHandler(us, ts)
	r := chi.NewRouter()
	r.Post("/api/auth/register", h.Register)

	body := `{"username":"alice","email":"alice@test.com","password":"` + testutil.FixturePassword("alice-reg") + `"}`
	w := doRequest(r, "POST", "/api/auth/register", body)
	if w.Code != http.StatusCreated {
		t.Fatalf(statusFmt, w.Code, w.Body.String())
	}
	resp := decodeBody(t, w.Body.String())
	assertExpiresNear(t, resp["expiresAt"], time.Now().Add(sessionTokenTTL))

	stored := ts.tokens[auth.HashToken(resp["token"].(string))]
	if stored == nil || stored.ExpiresAt == nil || stored.Scopes != "session" {
		t.Errorf("registration token must be a bounded session token: %+v", stored)
	}
}
