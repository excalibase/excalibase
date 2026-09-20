package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

// fakeDBEndpoints is a scripted DBEndpointAPI: it records the calls the
// handler makes and returns whatever the test set up.
type fakeDBEndpoints struct {
	view        service.DBEndpointView
	err         error
	publicCalls []bool
	tlsCalls    []bool
}

func (f *fakeDBEndpoints) Describe(context.Context, string) (service.DBEndpointView, error) {
	return f.view, f.err
}

func (f *fakeDBEndpoints) SetPublic(_ context.Context, _ string, public bool) (service.DBEndpointView, error) {
	f.publicCalls = append(f.publicCalls, public)
	f.view.Enabled = public
	return f.view, f.err
}

func (f *fakeDBEndpoints) SetRequireTLS(_ context.Context, _ string, requireTLS bool) (service.DBEndpointView, error) {
	f.tlsCalls = append(f.tlsCalls, requireTLS)
	f.view.RequireTLS = requireTLS
	return f.view, f.err
}

func dbEndpointRouter(api DBEndpointAPI) http.Handler {
	h := &ProvisioningHandler{dbEndpoints: api}
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/db-endpoint", func(r chi.Router) {
		r.Get("/", h.GetDBEndpoint)
		r.Put("/", h.PutDBEndpoint)
	})
	return r
}

func dbEndpointCall(t *testing.T, api DBEndpointAPI, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/api/projects/proj-abc/db-endpoint/", reader)
	w := httptest.NewRecorder()
	dbEndpointRouter(api).ServeHTTP(w, req)
	return w
}

func decodeDBEndpoint(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return out
}

func TestGetDBEndpointRendersEverythingAClientNeeds(t *testing.T) {
	view := service.DBEndpointView{
		ProjectID: "proj-abc", Enabled: true, Available: true,
		Host: "proj-abc.db.excalibase.io", Port: 30111, RequireTLS: true,
		Database: "appdb", Username: "app_user",
		CACertificate: "-----BEGIN CERTIFICATE-----",
	}
	view.Connection.RequireTLS = "postgresql://app_user@proj-abc.db.excalibase.io:30111/appdb?sslmode=verify-full"
	view.Connection.AllowPlaintext = "postgresql://app_user@proj-abc.db.excalibase.io:30111/appdb?sslmode=prefer"
	view.Internal.Host = "proj-abc-postgres-rw.org-proj-abc.svc.cluster.local"
	view.Internal.Port = 5432
	view.Internal.ConnectionString = "postgresql://app_user@proj-abc-postgres-rw.org-proj-abc.svc.cluster.local:5432/appdb?sslmode=prefer"

	w := dbEndpointCall(t, &fakeDBEndpoints{view: view}, http.MethodGet, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	body := decodeDBEndpoint(t, w)
	for _, key := range []string{"projectId", "publicEnabled", "available", "host", "port", "requireTls", "database", "username", "connectionStrings", "caCertificate", "internal"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("response is missing %q: %v", key, body)
		}
	}
	conn := body["connectionStrings"].(map[string]interface{})
	if !strings.Contains(conn["requireTls"].(string), "verify-full") {
		t.Fatalf("connectionStrings.requireTls = %v", conn["requireTls"])
	}
	if !strings.Contains(conn["allowPlaintext"].(string), "prefer") {
		t.Fatalf("connectionStrings.allowPlaintext = %v", conn["allowPlaintext"])
	}
	internal := body["internal"].(map[string]interface{})
	if internal["port"].(float64) != 5432 {
		t.Fatalf("internal.port = %v", internal["port"])
	}
}

func TestPutDBEndpointAppliesTheTLSChoiceBeforePublishing(t *testing.T) {
	api := &fakeDBEndpoints{}
	w := dbEndpointCall(t, api, http.MethodPut, `{"publicEnabled":true,"requireTls":false}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if len(api.tlsCalls) != 1 || api.tlsCalls[0] {
		t.Fatalf("tls calls = %v", api.tlsCalls)
	}
	if len(api.publicCalls) != 1 || !api.publicCalls[0] {
		t.Fatalf("public calls = %v", api.publicCalls)
	}
}

func TestPutDBEndpointLeavesUnmentionedFieldsAlone(t *testing.T) {
	api := &fakeDBEndpoints{}
	if w := dbEndpointCall(t, api, http.MethodPut, `{"requireTls":true}`); w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if len(api.publicCalls) != 0 {
		t.Fatalf("a body that says nothing about the public endpoint must not change it: %v", api.publicCalls)
	}
}

func TestPutDBEndpointRejectsAnUnreadableBody(t *testing.T) {
	if w := dbEndpointCall(t, &fakeDBEndpoints{}, http.MethodPut, `{`); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestDBEndpointSurfaceIsUnavailableWhenUnwired(t *testing.T) {
	h := &ProvisioningHandler{}
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/db-endpoint", func(r chi.Router) {
		r.Get("/", h.GetDBEndpoint)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/projects/proj-abc/db-endpoint/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestDBEndpointErrorsMapToTheirMeaning(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
	}{
		{"not configured", service.ErrDBEndpointNotConfigured, http.StatusServiceUnavailable},
		{"unsupported mode", service.ErrDBEndpointUnsupported, http.StatusConflict},
		{"ports exhausted", storage.ErrDBEndpointPortsExhausted, http.StatusServiceUnavailable},
		{"not observed", service.ErrDBEndpointNotObserved, http.StatusBadGateway},
		{"anything else", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := dbEndpointCall(t, &fakeDBEndpoints{err: tc.err}, http.MethodGet, "")
			if w.Code != tc.code {
				t.Fatalf("status = %d, want %d", w.Code, tc.code)
			}
		})
	}
}

func TestDBEndpointRefusesAnInvalidProjectId(t *testing.T) {
	h := &ProvisioningHandler{dbEndpoints: &fakeDBEndpoints{}}
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/db-endpoint", func(r chi.Router) {
		r.Get("/", h.GetDBEndpoint)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/projects/..%2Fetc/db-endpoint/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}
