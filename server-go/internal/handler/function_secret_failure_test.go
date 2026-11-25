package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/go-chi/chi/v5"
)

const (
	secretProject = "proj_p1"
	vaultLeak     = "vault-internal.cluster.local"
	secretFnCode  = "export default () => new Response('v1')"
	secretsPath   = "/api/projects/" + secretProject + "/functions/secrets"
)

// flakyVault reads and writes through fakeVault until broken is set.
type flakyVault struct {
	*fakeVault
	broken atomic.Bool
}

func (f *flakyVault) Get(p string) (map[string]string, error) {
	if f.broken.Load() {
		return nil, errors.New("dial " + vaultLeak + ": connection refused")
	}
	return f.fakeVault.Get(p)
}

func (f *flakyVault) Put(p string, d map[string]string) error {
	if f.broken.Load() {
		return errors.New("dial " + vaultLeak + ": connection refused")
	}
	return f.fakeVault.Put(p, d)
}

func secretHandler(t *testing.T, failDeploys *atomic.Bool) (*FunctionHandler, *flakyVault, chi.Router) {
	t.Helper()
	runtime, _ := functionRuntimeServer(t, failDeploys)
	h, _ := newFunctionHandlerFor(t, runtime.URL)
	vault := &flakyVault{fakeVault: newFakeVault()}
	h.secrets = edgefn.NewSecretsStore(vault)
	router := chi.NewRouter()
	router.Post("/api/projects/{projectId}/functions", h.Create)
	router.Post("/api/projects/{projectId}/functions/secrets", h.SetSecret)
	router.Delete("/api/projects/{projectId}/functions/secrets/{key}", h.DeleteSecret)
	return h, vault, router
}

func postSecret(router chi.Router) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", secretsPath, strings.NewReader(`{"key":"API_KEY","value":"s3cret-value"}`))
	router.ServeHTTP(w, req)
	return w
}

func assertCustomerSafe(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	for _, leak := range []string{vaultLeak, "connection refused", "s3cret-value"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Fatalf("response leaks %q: %s", leak, w.Body.String())
		}
	}
}

// A function must not ship without its secrets because the vault was down.
func TestCreateFunction_UnreadableSecretsFailTheDeployAndLeaveNothing(t *testing.T) {
	var failDeploys atomic.Bool
	h, vault, router := secretHandler(t, &failDeploys)
	vault.broken.Store(true)

	w := deployFunction(router, secretFnCode)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503: %s", w.Code, w.Body.String())
	}
	assertCustomerSafe(t, w)
	if got, _ := h.store.Get(secretProject, "hello"); got != nil {
		t.Fatal("the failed deploy left its function record behind")
	}
}

func TestSetSecret_VaultFailureIsAServiceErrorNotABadRequest(t *testing.T) {
	var failDeploys atomic.Bool
	_, vault, router := secretHandler(t, &failDeploys)
	vault.broken.Store(true)

	w := postSecret(router)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503: %s", w.Code, w.Body.String())
	}
	assertCustomerSafe(t, w)
}

func TestSetSecret_RefusedKeyStaysABadRequest(t *testing.T) {
	var failDeploys atomic.Bool
	_, _, router := secretHandler(t, &failDeploys)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", secretsPath, strings.NewReader(`{"key":"bad key!","value":"v"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", w.Code, w.Body.String())
	}
}

// The secret is stored, so the answer says what did not happen: the running
// functions still hold the old value.
func TestSetSecret_FailedRedeployIsReported(t *testing.T) {
	var failDeploys atomic.Bool
	_, _, router := secretHandler(t, &failDeploys)
	if w := deployFunction(router, secretFnCode); w.Code != http.StatusCreated {
		t.Fatalf("setup deploy: %d %s", w.Code, w.Body.String())
	}
	failDeploys.Store(true)

	w := postSecret(router)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502: %s", w.Code, w.Body.String())
	}
	assertCustomerSafe(t, w)
	if !strings.Contains(w.Body.String(), "saved") {
		t.Fatalf("answer %q does not say the secret was saved", w.Body.String())
	}
}

func TestDeleteSecret_FailedRedeployIsReported(t *testing.T) {
	var failDeploys atomic.Bool
	_, _, router := secretHandler(t, &failDeploys)
	if w := deployFunction(router, secretFnCode); w.Code != http.StatusCreated {
		t.Fatalf("setup deploy: %d %s", w.Code, w.Body.String())
	}
	if w := postSecret(router); w.Code != http.StatusOK {
		t.Fatalf("setup secret: %d %s", w.Code, w.Body.String())
	}
	failDeploys.Store(true)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("DELETE", secretsPath+"/API_KEY", nil))

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502: %s", w.Code, w.Body.String())
	}
}

func TestSetSecret_SuccessStillAnswersOK(t *testing.T) {
	var failDeploys atomic.Bool
	_, _, router := secretHandler(t, &failDeploys)
	if w := deployFunction(router, secretFnCode); w.Code != http.StatusCreated {
		t.Fatalf("setup deploy: %d", w.Code)
	}
	if w := postSecret(router); w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}
