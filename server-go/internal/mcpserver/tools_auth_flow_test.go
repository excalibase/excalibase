package mcpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// testPassword is a made-up value for the fake auth API, nobody's secret.
const testPassword = "Zq-test-only-pw-1" //gitleaks:allow fabricated test password

const (
	authBase     = "/auth/acme/" + testProjectA
	registerPath = authBase + "/register"
	loginPath    = authBase + "/token"
)

// authPlane is the project's auth API: register and password sign-in.
type authPlane struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   []string
	answer   func(w http.ResponseWriter, r *http.Request, body map[string]string)
	allowed  string
}

func (a *authPlane) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	a.requests = append(a.requests, r)
	a.bodies = append(a.bodies, string(raw))
	a.mu.Unlock()
	if r.Header.Get("Origin") == a.allowed && a.allowed != "" {
		w.Header().Set("Access-Control-Allow-Origin", a.allowed)
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var body map[string]string
	_ = json.Unmarshal(raw, &body)
	w.Header().Set("Content-Type", "application/json")
	a.answer(w, r, body)
}

const (
	issuedAccessToken  = "eyJhbGciOiJIUzI1NiJ9.eyJwcm9qZWN0SWQiOiJwcm9qLWEiLCJyb2xlIjoidXNlciIsInVzZXJJZCI6IjQyIn0.c2ln" //gitleaks:allow fabricated unsigned token
	issuedRefreshToken = "refresh-value-that-must-not-be-echoed"                                                         //gitleaks:allow fabricated
)

func signedIn(w http.ResponseWriter, _ *http.Request, _ map[string]string) {
	_, _ = w.Write([]byte(`{"accessToken":"` + issuedAccessToken + `","refreshToken":"` + issuedRefreshToken +
		`","tokenType":"Bearer","expiresIn":3600,"user":{"id":"42","email":"tester@example.test","fullName":"Test Er"}}`))
}

func authSession(t *testing.T, plane *authPlane, caller Caller) *mcp.ClientSession {
	t.Helper()
	server := httptest.NewServer(plane)
	t.Cleanup(server.Close)
	routes := newFakeRoutes()
	routes.on(http.MethodGet, projectsA+"/info/", 200, `{"projectId":"proj-a","orgSlug":"acme"}`)
	settings := testSettings
	settings.DataPlaneURL = server.URL
	return sessionWith(t, routes, &recordingAudit{}, caller, settings)
}

func flowArgs(flow string) map[string]any {
	return map[string]any{"project_id": testProjectA, "flow": flow, "email": "tester@example.test", "password": testPassword}
}

func TestAuthFlowLoginReportsTheOutcomeAndNeverTheTokens(t *testing.T) {
	plane := &authPlane{answer: signedIn}
	cs := authSession(t, plane, writeCaller())
	res := callTool(t, cs, "test_auth_flow", flowArgs("login"))
	out := structured(t, res)
	whole := resultText(res)
	for _, secret := range []string{testPassword, issuedAccessToken, issuedRefreshToken} {
		if strings.Contains(whole, secret) {
			t.Fatalf("the answer echoes a secret: %s", whole)
		}
	}
	claims, _ := out["claims"].(map[string]any)
	if out["outcome"] != "signed_in" || out["tokensIssued"] != true || claims["userId"] != "42" || claims["role"] != "user" {
		t.Fatalf("out = %v", out)
	}
	if plane.requests[0].URL.Path != loginPath || plane.bodies[0] != `{"email":"tester@example.test","grant_type":"password","password":"`+testPassword+`"}` {
		t.Fatalf("request %s %s", plane.requests[0].URL.Path, plane.bodies[0])
	}
}

func TestAuthFlowRegisterSendsTheFullNameAndReportsVerification(t *testing.T) {
	plane := &authPlane{answer: func(w http.ResponseWriter, _ *http.Request, _ map[string]string) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"emailVerificationRequired":true,"message":"check your email","user":{"id":"43","email":"tester@example.test"}}`))
	}}
	cs := authSession(t, plane, writeCaller())
	args := flowArgs("register")
	args["full_name"] = "Test Er"
	out := structured(t, callTool(t, cs, "test_auth_flow", args))
	if out["outcome"] != "verification_required" || out["tokensIssued"] != false || plane.requests[0].URL.Path != registerPath {
		t.Fatalf("out = %v", out)
	}
	if !strings.Contains(plane.bodies[0], `"fullName":"Test Er"`) {
		t.Fatalf("body = %s", plane.bodies[0])
	}
}

func TestAuthFlowRejectionDoesNotEchoThePassword(t *testing.T) {
	plane := &authPlane{answer: func(w http.ResponseWriter, _ *http.Request, body map[string]string) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"wrong password ` + body["password"] + ` for tester"}`))
	}}
	cs := authSession(t, plane, writeCaller())
	res := callTool(t, cs, "test_auth_flow", flowArgs("login"))
	out := structured(t, res)
	if out["outcome"] != "rejected" || out["status"] != float64(401) || strings.Contains(resultText(res), testPassword) {
		t.Fatalf("out = %v", out)
	}
}

func TestAuthFlowChecksTheBrowsersPreflightFromTheOrigin(t *testing.T) {
	for origin, wantBlocked := range map[string]bool{"https://web.example.test": false, "http://localhost:5173": true} {
		plane := &authPlane{answer: signedIn, allowed: "https://web.example.test"}
		cs := authSession(t, plane, writeCaller())
		args := flowArgs("login")
		args["origin"] = origin
		out := structured(t, callTool(t, cs, "test_auth_flow", args))
		cors, _ := out["cors"].(map[string]any)
		if (cors["blocked"] != nil) != wantBlocked {
			t.Errorf("%s: cors = %v", origin, cors)
		}
		if plane.requests[0].Method != http.MethodOptions || plane.requests[1].Header.Get("Origin") != origin {
			t.Errorf("%s: want a preflight, then the request with the origin", origin)
		}
	}
}

func TestAuthFlowRefusesBadInputBeforeAnyRequest(t *testing.T) {
	plane := &authPlane{answer: signedIn}
	cs := authSession(t, plane, writeCaller())
	for name, mutate := range map[string]func(map[string]any){
		"an unknown flow":      func(a map[string]any) { a["flow"] = "reset" },
		"no password":          func(a map[string]any) { a["password"] = "" },
		"an email with a line": func(a map[string]any) { a["email"] = "a@b.test\r\nX: 1" },
		"no email address":     func(a map[string]any) { a["email"] = "tester" },
		"a bad origin":         func(a map[string]any) { a["origin"] = "https://x.test/path" },
	} {
		args := flowArgs("login")
		mutate(args)
		if res := callTool(t, cs, "test_auth_flow", args); !res.IsError {
			t.Errorf("%s was not refused", name)
		}
	}
	if len(plane.requests) != 0 {
		t.Fatalf("a refused flow reached the auth API: %d", len(plane.requests))
	}
}

func TestAuthFlowOnAReadOnlyConnectionNeverRegisters(t *testing.T) {
	plane := &authPlane{answer: signedIn}
	cs := authSession(t, plane, readOnlyCaller())
	if res := callTool(t, cs, "test_auth_flow", flowArgs("register")); !res.IsError {
		t.Fatal("a read-only connection created a user")
	}
	if len(plane.requests) != 0 {
		t.Fatalf("reached the auth API: %d", len(plane.requests))
	}
	structured(t, callTool(t, cs, "test_auth_flow", flowArgs("login")))
}

func TestAuthFlowAuditKeepsNoArguments(t *testing.T) {
	plane := &authPlane{answer: signedIn}
	server := httptest.NewServer(plane)
	defer server.Close()
	routes := newFakeRoutes()
	routes.on(http.MethodGet, projectsA+"/info/", 200, `{"projectId":"proj-a","orgSlug":"acme"}`)
	settings := testSettings
	settings.DataPlaneURL = server.URL
	audit := &recordingAudit{}
	cs := sessionWith(t, routes, audit, writeCaller(), settings)
	structured(t, callTool(t, cs, "test_auth_flow", flowArgs("login")))
	for _, entry := range audit.entries {
		if strings.Contains(entry.Details, testPassword) || strings.Contains(entry.Details, "tester@example.test") {
			t.Fatalf("audit keeps arguments: %s", entry.Details)
		}
	}
}

func authFlowRouteCases() []routeCase {
	return []routeCase{{
		name: "test_auth_flow", tool: "test_auth_flow", args: flowArgs("login"),
		setup: func(f *fakeRoutes) {
			f.on(http.MethodGet, projectsA+"/info/", 200, `{"projectId":"proj-a","orgSlug":"acme"}`)
		},
		plane:  &authPlane{answer: signedIn},
		expect: []string{"GET " + projectsA + "/info/"},
	}}
}
