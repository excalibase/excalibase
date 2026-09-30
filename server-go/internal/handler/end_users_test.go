package handler

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/endusers"
	"github.com/excalibase/provisioning-poc/internal/sdkkeys"
)

type signingKeyVault map[string]map[string]string

func (v signingKeyVault) Get(path string) (map[string]string, error) {
	if data, ok := v[path]; ok {
		return data, nil
	}
	return nil, errors.New("not found")
}

// authCall is what the auth stub saw of one request.
type authCall struct {
	method, path, query, body string
	header                    http.Header
}

// endUserAuth stands in for excalibase-auth's end-user routes.
type endUserAuth struct {
	status int
	answer string
	calls  []authCall
}

func (a *endUserAuth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	a.calls = append(a.calls, authCall{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, body: string(body), header: r.Header.Clone()})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(a.status)
	_, _ = w.Write([]byte(a.answer))
}

type endUsersFixture struct {
	router chi.Router
	auth   *endUserAuth
	audit  *sdkKeyAudit
	key    *ecdsa.PrivateKey
}

// newEndUsersFixture wires the real signer and client against an auth stub,
// so a test sees the exact request and token auth would receive.
func newEndUsersFixture(t *testing.T, status int, answer string) *endUsersFixture {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalECPrivateKey(key)
	vault := signingKeyVault{"pki/signing/private": {"key": string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))}}
	stub := &endUserAuth{status: status, answer: answer}
	server := httptest.NewServer(stub)
	t.Cleanup(server.Close)
	client := endusers.NewClient(server.URL, sdkkeys.NewSigner(vault), server.Client())
	router, audit := endUsersRouter(client, "admin-1")
	return &endUsersFixture{router: router, auth: stub, audit: audit, key: key}
}

func endUsersRouter(manager EndUserManager, actor string) (chi.Router, *sdkKeyAudit) {
	instances := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj-a":      {ProjectID: "proj-a", OrgID: "org-1"},
		"proj-orphan": {ProjectID: "proj-orphan", OrgID: "org-gone"},
	}}
	audit := &sdkKeyAudit{}
	h := NewEndUsersHandler(manager, instances, sdkKeyOrgs{orgs: map[string]*domain.Org{"org-1": {ID: "org-1", Slug: "acme"}}}, audit)
	r := chi.NewRouter()
	if actor != "" {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, r.WithContext(auth.SetUser(r.Context(), &domain.User{ID: actor})))
			})
		})
	}
	r.Route("/api/projects/{projectId}/end-users", h.Routes)
	return r, audit
}

// studioRequest carries the Studio session the way a browser sends it.
func (f *endUsersFixture) studioRequest(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer studio-session-token")
	req.Header.Set("Cookie", "excalibase_session=studio-session-cookie")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

// verifiedClaims checks the bearer auth received against the platform key.
func (f *endUsersFixture) verifiedClaims(t *testing.T, call authCall) jwt.MapClaims {
	t.Helper()
	raw := strings.TrimPrefix(call.header.Get("Authorization"), "Bearer ")
	claims := jwt.MapClaims{}
	if _, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) { return &f.key.PublicKey, nil },
		jwt.WithValidMethods([]string{"ES256"})); err != nil {
		t.Fatalf("auth received a token the platform key does not verify: %v", err)
	}
	return claims
}

func assertUserAdminToken(t *testing.T, claims jwt.MapClaims) {
	t.Helper()
	if claims["token_use"] != "user_admin" || claims["actor"] != "admin-1" {
		t.Fatalf("claims: %v", claims)
	}
	aud, _ := claims.GetAudience()
	if !slices.Equal([]string(aud), []string{"excalibase-auth:proj-a"}) {
		t.Fatalf("aud = %v", aud)
	}
	iat, _ := claims.GetIssuedAt()
	exp, _ := claims.GetExpirationTime()
	if lifetime := exp.Sub(iat.Time); lifetime <= 0 || lifetime > 60*time.Second {
		t.Fatalf("lifetime = %v", lifetime)
	}
}

func assertNoStudioCredential(t *testing.T, call authCall) {
	t.Helper()
	if call.header.Get("Cookie") != "" || strings.Contains(call.header.Get("Authorization"), "studio-session") {
		t.Fatalf("the Studio session reached auth: %v", call.header)
	}
}

func TestEndUsersListRelaysAuthsPageWithAUserAdminToken(t *testing.T) {
	f := newEndUsersFixture(t, http.StatusOK, `{"users":[{"id":3,"email":"a@x.test","role":"user","allowedRoles":["user"]}],"total":1}`)

	w := f.studioRequest("GET", "/api/projects/proj-a/end-users/?limit=25&offset=50&role=admin", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"email":"a@x.test"`) {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("a list of end users' emails must not be cached")
	}
	call := f.auth.calls[0]
	if call.method != http.MethodGet || call.path != "/auth/acme/proj-a/users" || call.query != "limit=25&offset=50" {
		t.Fatalf("auth saw %+v", call)
	}
	assertUserAdminToken(t, f.verifiedClaims(t, call))
	assertNoStudioCredential(t, call)
	if len(f.audit.entries) != 0 {
		t.Fatalf("a read was audited: %+v", f.audit.entries)
	}
}

func TestEndUsersListValidatesPaging(t *testing.T) {
	f := newEndUsersFixture(t, http.StatusOK, `{"users":[]}`)
	for _, query := range []string{"limit=0", "limit=-1", "limit=1001", "limit=x", "offset=-1", "offset=1.5", "offset=99999999999"} {
		w := f.studioRequest("GET", "/api/projects/proj-a/end-users/?"+query, "")
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"invalid_request"`) {
			t.Errorf("%s: %d %s", query, w.Code, w.Body.String())
		}
	}
	if len(f.auth.calls) != 0 {
		t.Fatalf("invalid paging reached auth: %+v", f.auth.calls)
	}
}

func TestEndUsersSetRoleRelaysTheChangeAndAuditsIt(t *testing.T) {
	f := newEndUsersFixture(t, http.StatusOK, `{"id":7,"email":"b@x.test","role":"editor","allowedRoles":["editor","user"]}`)

	w := f.studioRequest("PUT", "/api/projects/proj-a/end-users/7/role", `{"role":"editor","allowedRoles":["editor","user"]}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"role":"editor"`) {
		t.Fatalf("set role: %d %s", w.Code, w.Body.String())
	}
	call := f.auth.calls[0]
	if call.method != http.MethodPut || call.path != "/auth/acme/proj-a/users/7/role" {
		t.Fatalf("auth saw %+v", call)
	}
	var sent endusers.RoleChange
	if err := json.Unmarshal([]byte(call.body), &sent); err != nil || sent.Role != "editor" || !slices.Equal(sent.AllowedRoles, []string{"editor", "user"}) {
		t.Fatalf("auth received %s", call.body)
	}
	assertUserAdminToken(t, f.verifiedClaims(t, call))
	assertNoStudioCredential(t, call)

	entry := f.onlyAuditEntry(t)
	details := auditDetails(t, entry)
	if entry.Action != "end_user.role.set" || entry.Resource != "end_user" || entry.ResourceID != "proj-a" || entry.UserID != "admin-1" ||
		details["outcome"] != "success" || details["userId"] != float64(7) || details["role"] != "editor" ||
		!slices.Equal(stringSlice(details["allowedRoles"]), []string{"editor", "user"}) {
		t.Fatalf("audit: %+v %v", entry, details)
	}
}

func TestEndUsersSetRolePassesAuthRefusalsThroughAndAuditsThem(t *testing.T) {
	cases := []struct {
		authStatus int
		answer     string
		wantStatus int
		wantCode   string
	}{
		{http.StatusNotFound, `{"error":"user not found","code":"user_not_found","status":404}`, http.StatusNotFound, "user_not_found"},
		{http.StatusUnprocessableEntity, `{"error":"unknown role","code":"unknown_role","status":422}`, http.StatusUnprocessableEntity, "unknown_role"},
		{http.StatusForbidden, `{"error":"forbidden","status":403}`, http.StatusForbidden, ""},
	}
	for _, tc := range cases {
		f := newEndUsersFixture(t, tc.authStatus, tc.answer)
		w := f.studioRequest("PUT", "/api/projects/proj-a/end-users/7/role", `{"role":"editor"}`)
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != tc.wantStatus || (tc.wantCode != "" && body["code"] != tc.wantCode) {
			t.Errorf("auth %d: got %d %s", tc.authStatus, w.Code, w.Body.String())
			continue
		}
		details := auditDetails(t, f.onlyAuditEntry(t))
		if details["outcome"] != "refused" || details["status"] != float64(tc.authStatus) || details["role"] != "editor" {
			t.Errorf("auth %d: audit %v", tc.authStatus, details)
		}
	}
}

func TestEndUsersAuthFailuresAreA502WithAStableCode(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusServiceUnavailable} {
		f := newEndUsersFixture(t, status, `{"error":"db: connection refused at 10.0.0.4"}`)
		w := f.studioRequest("PUT", "/api/projects/proj-a/end-users/7/role", `{"role":"editor"}`)
		if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), `"code":"auth_unavailable"`) ||
			strings.Contains(w.Body.String(), "10.0.0.4") {
			t.Errorf("auth %d: got %d %s", status, w.Code, w.Body.String())
		}
		if details := auditDetails(t, f.onlyAuditEntry(t)); details["outcome"] != "auth_unavailable" {
			t.Errorf("auth %d: audit %v", status, details)
		}
		if w := f.studioRequest("GET", "/api/projects/proj-a/end-users/", ""); w.Code != http.StatusBadGateway {
			t.Errorf("list with auth %d: got %d", status, w.Code)
		}
	}

	router, audit := endUsersRouter(endusers.NewClient("http://127.0.0.1:1", fixedSigner{}, &http.Client{Timeout: time.Second}), "admin-1")
	if w := doRequest(router, "PUT", "/api/projects/proj-a/end-users/7/role", `{"role":"editor"}`); w.Code != http.StatusBadGateway ||
		!strings.Contains(w.Body.String(), `"code":"auth_unavailable"`) {
		t.Fatalf("unreachable: %d %s", w.Code, w.Body.String())
	}
	if len(audit.entries) != 1 {
		t.Fatalf("audit: %+v", audit.entries)
	}
}

// A 401 from auth means our own token was refused. Relaying it would read to
// Studio as an expired session and sign the admin out.
func TestEndUsersAuthRejectingOurTokenIsNotTheCallersSessionEnding(t *testing.T) {
	f := newEndUsersFixture(t, http.StatusUnauthorized, `{"error":"invalid token","status":401}`)
	w := f.studioRequest("PUT", "/api/projects/proj-a/end-users/7/role", `{"role":"editor"}`)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), `"code":"auth_rejected_credential"`) {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if details := auditDetails(t, f.onlyAuditEntry(t)); details["outcome"] != "refused" || details["status"] != float64(401) {
		t.Fatalf("audit %v", details)
	}
}

func TestEndUsersSetRoleValidatesTheRequestAndAuditsTheRefusal(t *testing.T) {
	f := newEndUsersFixture(t, http.StatusOK, `{}`)
	cases := map[string]string{
		"/api/projects/proj-a/end-users/abc/role":                             `{"role":"editor"}`,
		"/api/projects/proj-a/end-users/0/role":                               `{"role":"editor"}`,
		"/api/projects/proj-a/end-users/-2/role":                              `{"role":"editor"}`,
		"/api/projects/proj-a/end-users/7/role":                               `not json`,
		"/api/projects/proj-a/end-users/" + strings.Repeat("9", 80) + "/role": `{"role":"editor"}`,
	}
	bodies := []string{
		`{}`,
		`{"role":""}`,
		`{"role":"Editor"}`,
		`{"role":"service"}`,
		`{"role":42}`,
		`{"role":"editor","admin":true}`,
		`{"role":"editor","allowedRoles":[]}`,
		`{"role":"editor","allowedRoles":["user"]}`,
		`{"role":"editor","allowedRoles":["editor","service"]}`,
		`{"role":"editor","allowedRoles":["editor","editor"]}`,
		`{"role":"editor","allowedRoles":"editor"}`,
		`{"role":"editor"} {"role":"admin"}`,
		`{"role":"editor","allowedRoles":["editor",` + strings.Repeat(`"r",`, 40) + `"z"]}`,
	}
	for _, body := range bodies {
		cases["/api/projects/proj-a/end-users/7/role#"+body] = body
	}
	for path, body := range cases {
		path, _, _ = strings.Cut(path, "#")
		w := f.studioRequest("PUT", path, body)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"invalid_request"`) {
			t.Errorf("%s %s: got %d %s", path, body, w.Code, w.Body.String())
		}
	}
	if len(f.auth.calls) != 0 {
		t.Fatalf("an invalid request reached auth: %+v", f.auth.calls)
	}
	if len(f.audit.entries) != len(cases) {
		t.Fatalf("audited %d of %d refused changes", len(f.audit.entries), len(cases))
	}
	for _, entry := range f.audit.entries {
		if details := auditDetails(t, entry); details["outcome"] != "invalid_request" || entry.UserID != "admin-1" ||
			len(details["userId"].(string)) > 64 {
			t.Fatalf("audit %+v %v", entry, details)
		}
	}
}

func TestEndUsersSetRoleWithoutAllowedRolesLetsAuthDefaultThem(t *testing.T) {
	f := newEndUsersFixture(t, http.StatusOK, `{"id":7,"email":"b@x.test","role":"editor","allowedRoles":["editor"]}`)
	if w := f.studioRequest("PUT", "/api/projects/proj-a/end-users/7/role", `{"role":"editor"}`); w.Code != http.StatusOK {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(f.auth.calls[0].body, "allowedRoles") {
		t.Fatalf("auth received %s", f.auth.calls[0].body)
	}
}

func TestEndUsersUnknownProjectOrOrgIsRefused(t *testing.T) {
	f := newEndUsersFixture(t, http.StatusOK, `{}`)
	if w := f.studioRequest("GET", "/api/projects/proj-missing/end-users/", ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown project: %d", w.Code)
	}
	if w := f.studioRequest("PUT", "/api/projects/proj-missing/end-users/7/role", `{"role":"editor"}`); w.Code != http.StatusNotFound {
		t.Errorf("set role in an unknown project: %d", w.Code)
	}
	if w := f.studioRequest("GET", "/api/projects/proj-orphan/end-users/", ""); w.Code != http.StatusInternalServerError {
		t.Errorf("project without an org: %d", w.Code)
	}
	if len(f.auth.calls) != 0 {
		t.Fatal("auth was called for a project that could not be resolved")
	}
}

func TestEndUsersWithoutAuthConfiguredIs503(t *testing.T) {
	router, _ := endUsersRouter(nil, "admin-1")
	for _, method := range []string{"GET", "PUT"} {
		path := "/api/projects/proj-a/end-users/"
		if method == "PUT" {
			path += "7/role"
		}
		if w := doRequest(router, method, path, `{"role":"editor"}`); w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: got %d, want 503", method, w.Code)
		}
	}
}

func TestEndUsersNeedASignedInActor(t *testing.T) {
	router, audit := endUsersRouter(endusers.NewClient("http://127.0.0.1:1", fixedSigner{}, http.DefaultClient), "")
	if w := doRequest(router, "GET", "/api/projects/proj-a/end-users/", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("list: got %d", w.Code)
	}
	if w := doRequest(router, "PUT", "/api/projects/proj-a/end-users/7/role", `{"role":"editor"}`); w.Code != http.StatusUnauthorized {
		t.Errorf("set role: got %d", w.Code)
	}
	if len(audit.entries) != 0 {
		t.Fatalf("audit: %+v", audit.entries)
	}
}

func TestEndUsersSigningFailureIsOurs(t *testing.T) {
	router, audit := endUsersRouter(endusers.NewClient("http://127.0.0.1:1", fixedSigner{err: errors.New("vault sealed")}, http.DefaultClient), "admin-1")
	w := doRequest(router, "PUT", "/api/projects/proj-a/end-users/7/role", `{"role":"editor"}`)
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "vault") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if details := auditDetails(t, audit.entries[0]); details["outcome"] != "error" {
		t.Fatalf("audit %v", details)
	}
}

func TestEndUsersChangeStandsWhenTheAuditWriteFails(t *testing.T) {
	f := newEndUsersFixture(t, http.StatusOK, `{"id":7,"role":"editor","allowedRoles":["editor"]}`)
	f.audit.err = errors.New("audit store down")
	if w := f.studioRequest("PUT", "/api/projects/proj-a/end-users/7/role", `{"role":"editor"}`); w.Code != http.StatusOK {
		t.Fatalf("got %d", w.Code)
	}
	f.onlyAuditEntry(t)
}

func TestEndUsersWithoutAnAuditWriter(t *testing.T) {
	instances := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{"proj-a": {ProjectID: "proj-a", OrgID: "org-1"}}}
	orgs := sdkKeyOrgs{orgs: map[string]*domain.Org{"org-1": {ID: "org-1", Slug: "acme"}}}
	stub := &endUserAuth{status: http.StatusOK, answer: `{"id":7,"role":"editor","allowedRoles":["editor"]}`}
	server := httptest.NewServer(stub)
	t.Cleanup(server.Close)
	h := NewEndUsersHandler(endusers.NewClient(server.URL, fixedSigner{}, server.Client()), instances, orgs, nil)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.SetUser(r.Context(), &domain.User{ID: "admin-1"})))
		})
	})
	r.Route("/api/projects/{projectId}/end-users", h.Routes)
	if w := doRequest(r, "PUT", "/api/projects/proj-a/end-users/7/role", `{"role":"editor"}`); w.Code != http.StatusOK {
		t.Fatalf("got %d", w.Code)
	}
}

type fixedSigner struct{ err error }

func (s fixedSigner) SignUserAdmin(string, string, string) (string, error) { return "t", s.err }

func (f *endUsersFixture) onlyAuditEntry(t *testing.T) *domain.AuditEntry {
	t.Helper()
	if len(f.audit.entries) != 1 {
		t.Fatalf("audit entries = %d: %+v", len(f.audit.entries), f.audit.entries)
	}
	return f.audit.entries[0]
}

func auditDetails(t *testing.T, entry *domain.AuditEntry) map[string]any {
	t.Helper()
	details := map[string]any{}
	if err := json.Unmarshal([]byte(entry.Details), &details); err != nil {
		t.Fatalf("audit details %q: %v", entry.Details, err)
	}
	return details
}

func stringSlice(value any) []string {
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, _ := item.(string)
		out = append(out, text)
	}
	return out
}
