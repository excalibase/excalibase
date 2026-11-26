package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
)

const (
	functionSecretsPath = "/api/vault/secrets/projects/my-app/edgefn/secrets"
	otherSecretPath     = "/api/vault/secrets/projects/my-app/credentials/app"
	lockWait            = 150 * time.Millisecond
)

// serveInBackground runs the request and reports when it has answered.
func serveInBackground(r http.Handler, method, path, body string) <-chan int {
	answered := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set(sharedContentType, sharedMIMEJSON)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		answered <- w.Code
	}()
	return answered
}

// EXC-897: a platform admin's write to a project's function secrets reads and
// replaces the same map the store's set and delete do, so it waits its turn.
func TestVaultFunctionSecretsWritesWaitForTheProjectLock(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		r, v := setupVaultRouter(t)
		if _, err := v.Init(1, 1); err != nil {
			t.Fatal(err)
		}
		unlock := edgefn.LockProjectSecrets("my-app")

		answered := serveInBackground(r, method, functionSecretsPath, `{"KEY":"value"}`)

		select {
		case code := <-answered:
			t.Fatalf("%s answered %d while another change to the project's secrets was running", method, code)
		case <-time.After(lockWait):
		}
		unlock()
		select {
		case code := <-answered:
			if code != http.StatusOK {
				t.Fatalf("%s answered %d once the lock was free", method, code)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s never answered after the lock was free", method)
		}
	}
}

func TestVaultOtherSecretsAreNotHeldByTheFunctionSecretsLock(t *testing.T) {
	r, v := setupVaultRouter(t)
	if _, err := v.Init(1, 1); err != nil {
		t.Fatal(err)
	}
	defer edgefn.LockProjectSecrets("my-app")()

	select {
	case code := <-serveInBackground(r, http.MethodPut, otherSecretPath, `{"password":"x"}`):
		if code != http.StatusOK {
			t.Fatalf("answered %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a secret that is not the function secrets waited for their lock")
	}
}
