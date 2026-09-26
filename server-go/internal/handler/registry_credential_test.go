package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

const registryTestSecret = "ghp_write_only"

type fakeRegistryCredentials struct {
	stored  map[string]apphost.RegistryCredential
	err     error
	removed []string
}

func (f *fakeRegistryCredentials) Set(projectID, registry string, cred apphost.RegistryCredential) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if cred.Validate() != nil {
		return "", fmt.Errorf("%w: bad", service.ErrInvalidRegistryCredential)
	}
	f.stored[projectID+"/"+registry] = cred
	return registry, nil
}

func (f *fakeRegistryCredentials) List(string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []string{"ghcr.io"}, nil
}

func (f *fakeRegistryCredentials) Remove(_ context.Context, projectID, registry string) error {
	if f.err != nil {
		return f.err
	}
	f.removed = append(f.removed, projectID+"/"+registry)
	return nil
}

func registryRouter(creds *fakeRegistryCredentials) chi.Router {
	h := NewRegistryCredentialHandler(creds)
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/registry-credentials", h.Routes)
	return r
}

func registryRequest(r chi.Router, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/projects/"+appTestProject+"/registry-credentials"+path, strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestRegistryCredentialHandler_SetIsWriteOnly(t *testing.T) {
	creds := &fakeRegistryCredentials{stored: map[string]apphost.RegistryCredential{}}
	w := registryRequest(registryRouter(creds), http.MethodPut, "/ghcr.io",
		`{"username":"octocat","password":"`+registryTestSecret+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), registryTestSecret) || strings.Contains(w.Body.String(), "octocat") {
		t.Fatalf("the response carried the credential: %s", w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["registry"] != "ghcr.io" || body["set"] != true {
		t.Fatalf("body = %s", w.Body.String())
	}
	if creds.stored[appTestProject+"/ghcr.io"].Password != registryTestSecret {
		t.Fatalf("stored = %v", creds.stored)
	}
}

func TestRegistryCredentialHandler_ListAndRemove(t *testing.T) {
	creds := &fakeRegistryCredentials{stored: map[string]apphost.RegistryCredential{}}
	r := registryRouter(creds)
	w := registryRequest(r, http.MethodGet, "/", "")
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `[{"registry":"ghcr.io"}]` {
		t.Fatalf("list = %d %s", w.Code, w.Body.String())
	}
	w = registryRequest(r, http.MethodDelete, "/localhost:5000", "")
	if w.Code != http.StatusNoContent || creds.removed[0] != appTestProject+"/localhost:5000" {
		t.Fatalf("remove = %d %v", w.Code, creds.removed)
	}
}

func TestRegistryCredentialHandler_Refusals(t *testing.T) {
	creds := &fakeRegistryCredentials{stored: map[string]apphost.RegistryCredential{}}
	r := registryRouter(creds)
	for name, tc := range map[string]struct {
		body string
		code int
	}{
		"not json":         {`nope`, http.StatusBadRequest},
		"missing password": {`{"username":"u"}`, http.StatusBadRequest},
		"too large":        {`{"username":"u","password":"` + strings.Repeat("p", 20000) + `"}`, http.StatusBadRequest},
	} {
		if w := registryRequest(r, http.MethodPut, "/ghcr.io", tc.body); w.Code != tc.code {
			t.Errorf("%s: status %d, want %d", name, w.Code, tc.code)
		}
	}
	creds.err = errors.New("the credential could not be stored")
	for _, method := range []string{http.MethodPut, http.MethodGet, http.MethodDelete} {
		path := "/ghcr.io"
		if method == http.MethodGet {
			path = "/"
		}
		if w := registryRequest(r, method, path, `{"username":"u","password":"p"}`); w.Code != http.StatusInternalServerError {
			t.Errorf("%s: status %d, want 500", method, w.Code)
		}
	}
}

func TestRegistryCredentialHandler_RejectsAnInvalidProject(t *testing.T) {
	creds := &fakeRegistryCredentials{stored: map[string]apphost.RegistryCredential{}}
	req := httptest.NewRequest(http.MethodGet, "/api/projects/bad%20id/registry-credentials/", nil)
	w := httptest.NewRecorder()
	registryRouter(creds).ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d", w.Code)
	}
}

func TestRegistryCredentialHandler_NoVaultIsUnavailable(t *testing.T) {
	h := NewRegistryCredentialHandler(nil)
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/registry-credentials", h.Routes)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		path := "/ghcr.io"
		if method == http.MethodGet {
			path = "/"
		}
		if w := registryRequest(r, method, path, `{}`); w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status %d, want 503", method, w.Code)
		}
	}
}
