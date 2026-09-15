package handler

import (
	"context"
	"crypto/ecdsa"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

const (
	audTestProject = "proj_p1"
	audTestPrefix  = "excalibase:"
	audTestCorrect = audTestPrefix + audTestProject
)

// newAudTestHandler wires a FunctionHandler with a real ES256 vault key and a
// single deployed function at /functions/v1/proj_p1/secure.
func newAudTestHandler(t *testing.T) (*FunctionHandler, *ecdsa.PrivateKey, *chi.Mux) {
	t.Helper()
	v, priv := setupVaultWithSigningKey(t)
	store := edgefn.NewFunctionStore(t.TempDir())
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	h := NewFunctionHandler(store, secrets, client, &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		audTestProject: {ProjectID: audTestProject, OrgID: "default"},
	}}, nil, "")
	h.SetVault(v)
	store.Save(&edgefn.Function{
		ProjectID: audTestProject, ID: "secure", Name: "Secure",
		Files:  []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}},
		Active: true,
	})
	client.Deploy(context.Background(), edgefn.DeployRequest{ID: "proj_p1__secure", Code: "ok"})

	pub := chi.NewRouter()
	pub.HandleFunc(testPublicInvokeRoute, h.PublicInvoke)
	return h, priv, pub
}

func invokeWithToken(pub *chi.Mux, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", testSecureFnPath, nil)
	req.Header.Set("Authorization", sharedBearerPrefix+token)
	w := httptest.NewRecorder()
	pub.ServeHTTP(w, req)
	return w
}

func baseAudClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"iss":       "excalibase",
		"sub":       "user-alice",
		"projectId": audTestProject,
		"token_use": "access",
		"exp":       time.Now().Add(time.Hour).Unix(),
	}
}

func TestValidateProjectJWT_AudMatchingProjectAccepted(t *testing.T) {
	h, priv, pub := newAudTestHandler(t)
	h.SetAudienceRequirement(true, audTestPrefix)

	claims := baseAudClaims()
	claims["aud"] = []string{audTestCorrect}
	w := invokeWithToken(pub, signES256(t, priv, claims))

	if w.Code == http.StatusUnauthorized {
		t.Fatalf("matching aud should be accepted, got 401 body=%s", w.Body.String())
	}
}

func TestValidateProjectJWT_AudForDifferentProjectRejected(t *testing.T) {
	h, priv, pub := newAudTestHandler(t)
	h.SetAudienceRequirement(true, audTestPrefix)

	claims := baseAudClaims()
	claims["aud"] = []string{audTestPrefix + "proj_other"}
	w := invokeWithToken(pub, signES256(t, priv, claims))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("aud for another project should be 401, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), errCodeAudMismatch) {
		t.Errorf("body should carry %q, got %s", errCodeAudMismatch, w.Body.String())
	}
}

func TestValidateProjectJWT_MissingAudRejectedWhenRequired(t *testing.T) {
	h, priv, pub := newAudTestHandler(t)
	h.SetAudienceRequirement(true, audTestPrefix)

	w := invokeWithToken(pub, signES256(t, priv, baseAudClaims()))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("missing aud should be 401 when required, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), errCodeAudMismatch) {
		t.Errorf("body should carry %q, got %s", errCodeAudMismatch, w.Body.String())
	}
}

func TestValidateProjectJWT_MissingAudAcceptedWhenNotRequired(t *testing.T) {
	h, priv, pub := newAudTestHandler(t)
	h.SetAudienceRequirement(false, audTestPrefix)

	w := invokeWithToken(pub, signES256(t, priv, baseAudClaims()))

	if w.Code == http.StatusUnauthorized {
		t.Fatalf("missing aud should be accepted during phased rollout, got 401 body=%s", w.Body.String())
	}
}

// RFC 7519 allows aud to be a single string rather than an array.
func TestValidateProjectJWT_AudAsSingleStringAccepted(t *testing.T) {
	h, priv, pub := newAudTestHandler(t)
	h.SetAudienceRequirement(true, audTestPrefix)

	claims := baseAudClaims()
	claims["aud"] = audTestCorrect
	w := invokeWithToken(pub, signES256(t, priv, claims))

	if w.Code == http.StatusUnauthorized {
		t.Fatalf("string aud should be accepted, got 401 body=%s", w.Body.String())
	}
}

func TestValidateProjectJWT_AudArrayWithOneMatchAccepted(t *testing.T) {
	h, priv, pub := newAudTestHandler(t)
	h.SetAudienceRequirement(true, audTestPrefix)

	claims := baseAudClaims()
	claims["aud"] = []string{"https://other.example", audTestCorrect, "urn:something"}
	w := invokeWithToken(pub, signES256(t, priv, claims))

	if w.Code == http.StatusUnauthorized {
		t.Fatalf("aud array containing the project audience should be accepted, got 401 body=%s", w.Body.String())
	}
}

func TestValidateProjectJWT_EmptyAudArrayRejected(t *testing.T) {
	h, priv, pub := newAudTestHandler(t)
	h.SetAudienceRequirement(true, audTestPrefix)

	claims := baseAudClaims()
	claims["aud"] = []string{}
	w := invokeWithToken(pub, signES256(t, priv, claims))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("empty aud should be 401, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), errCodeAudMismatch) {
		t.Errorf("body should carry %q, got %s", errCodeAudMismatch, w.Body.String())
	}
}

// A refresh credential is never an API access token, even when every other
// claim (signature, projectId, aud) is perfect.
func TestValidateProjectJWT_RefreshTokenRejected(t *testing.T) {
	h, priv, pub := newAudTestHandler(t)
	h.SetAudienceRequirement(true, audTestPrefix)

	claims := baseAudClaims()
	claims["aud"] = []string{audTestCorrect}
	claims["token_use"] = "refresh"
	w := invokeWithToken(pub, signES256(t, priv, claims))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("refresh token should be 401, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), errCodeRefreshNotAccepted) {
		t.Errorf("body should carry %q, got %s", errCodeRefreshNotAccepted, w.Body.String())
	}
}

func TestNormalizeAudience(t *testing.T) {
	cases := []struct {
		name string
		raw  interface{}
		want []string
	}{
		{"nil", nil, nil},
		{"string", "a", []string{"a"}},
		{"empty string", "", nil},
		{"string slice", []string{"a", "b"}, []string{"a", "b"}},
		{"interface slice", []interface{}{"a", 1, "b"}, []string{"a", "b"}},
		{"empty slice", []interface{}{}, nil},
		{"unsupported", 42, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeAudience(tc.raw)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}
