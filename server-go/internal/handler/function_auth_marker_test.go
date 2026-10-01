package handler

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/go-chi/chi/v5"
)

// capturingRuntime answers every invoke with 200 and keeps the headers the
// gateway forwarded.
func capturingRuntime(t *testing.T) (*httptest.Server, *map[string]string) {
	t.Helper()
	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req edgefn.InvokeRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		for k := range seen {
			delete(seen, k)
		}
		for k, v := range req.Headers {
			seen[k] = v
		}
		_ = json.NewEncoder(w).Encode(edgefn.InvokeResponse{Status: 200, Body: `{}`})
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func newMarkerTestRouter(t *testing.T) (*chi.Mux, *map[string]string, func(claims map[string]interface{}) string) {
	t.Helper()
	v, priv := setupVaultWithSigningKey(t)
	store := edgefn.NewFunctionStore(t.TempDir())
	runtime, seen := capturingRuntime(t)
	h := NewFunctionHandler(store, edgefn.NewSecretsStore(v), edgefn.NewRuntimeClient(runtime.URL, ""),
		&inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{audTestProject: {ProjectID: audTestProject, OrgID: "default"}}}, nil, "")
	h.SetVault(v)
	h.SetAudienceRequirement(true, audTestPrefix)
	open := false
	for _, fn := range []*edgefn.Function{
		{ProjectID: audTestProject, ID: "secure", Name: "Secure", Active: true, Files: []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}}},
		{ProjectID: audTestProject, ID: "open", Name: "Open", Active: true, VerifyJwt: &open, Files: []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}}},
	} {
		if err := store.Save(fn); err != nil {
			t.Fatal(err)
		}
	}
	r := chi.NewRouter()
	r.HandleFunc(testPublicInvokeRoute, h.PublicInvoke)
	sign := func(claims map[string]interface{}) string {
		c := baseAudClaims()
		c["aud"] = []string{audTestCorrect}
		for k, val := range claims {
			c[k] = val
		}
		return signES256(t, priv, c)
	}
	return r, seen, sign
}

func invokeMarker(r *chi.Mux, fn, token string, extra map[string]string) {
	req := httptest.NewRequest("POST", "/functions/v1/"+audTestProject+"/"+fn, nil)
	if token != "" {
		req.Header.Set("Authorization", sharedBearerPrefix+token)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	r.ServeHTTP(httptest.NewRecorder(), req)
}

// ctx.auth is built only from a token the gateway verified: a function that
// checks the caller's role (a storage rule, EXC-518) must not be fooled by an
// unsigned token or a header the caller set itself.
func TestPublicInvoke_MarksOnlyAVerifiedToken(t *testing.T) {
	r, seen, sign := newMarkerTestRouter(t)

	invokeMarker(r, "secure", sign(map[string]interface{}{"role": "staff"}), nil)
	if (*seen)[authVerifiedHeader] != "1" {
		t.Fatalf("a verified token must be marked, headers=%v", *seen)
	}

	unsigned := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(`{"role":"staff"}`)) + "."
	invokeMarker(r, "open", unsigned, nil)
	if _, marked := (*seen)[authVerifiedHeader]; marked {
		t.Fatal("a function that does not verify tokens must not mark the caller's token")
	}

	invokeMarker(r, "open", "", map[string]string{authVerifiedHeader: "1", "x-excalibase-auth-verified": "1"})
	for k := range *seen {
		if http.CanonicalHeaderKey(k) == authVerifiedHeader {
			t.Fatalf("a caller-sent marker must be dropped, headers=%v", *seen)
		}
	}
}
