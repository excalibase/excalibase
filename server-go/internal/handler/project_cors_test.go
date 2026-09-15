package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

const (
	testCorsProject = "proj_cors1"
	testCorsPath    = "/api/projects/" + testCorsProject + "/cors"
	testCorsOriginA = "https://app.example.com"
	testCorsOriginB = "http://localhost:5173"
)

// memCorsStore is the in-memory ProjectCorsStore for handler tests.
type memCorsStore struct {
	origins map[string][]string
	err     error
}

func (m *memCorsStore) GetCorsOrigins(_ context.Context, projectID string) ([]string, error) {
	if m.err != nil {
		return nil, m.err
	}
	if o, ok := m.origins[projectID]; ok {
		return o, nil
	}
	return []string{}, nil
}

func (m *memCorsStore) SetCorsOrigins(_ context.Context, projectID string, origins []string) error {
	if m.err != nil {
		return m.err
	}
	m.origins[projectID] = origins
	return nil
}

type corsFixture struct {
	router  *chi.Mux
	handler *ProvisioningHandler
	cors    *memCorsStore
}

func setupCorsHandler(t *testing.T) corsFixture {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	svc := service.NewProvisioningService(store, provisioner.NewFactory(), nil)
	h := NewProvisioningHandler(svc, &adminOrgStore{})
	cors := &memCorsStore{origins: map[string][]string{}}
	h.SetCorsStore(cors)

	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/cors", func(r chi.Router) {
		r.Get("/", h.GetCors)
		r.Put("/", h.PutCors)
	})
	return corsFixture{router: r, handler: h, cors: cors}
}

func decodeCors(t *testing.T, body []byte) corsResponse {
	t.Helper()
	var out corsResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode cors response: %v (%s)", err, body)
	}
	return out
}

func corsBody(origins []string) map[string]interface{} {
	return map[string]interface{}{"allowedOrigins": origins}
}

func TestCors_DefaultIsNoOrigins(t *testing.T) {
	f := setupCorsHandler(t)
	w := doJSON(f.router, "GET", testCorsPath, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", w.Code, w.Body.String())
	}
	got := decodeCors(t, w.Body.Bytes())
	if len(got.AllowedOrigins) != 0 || got.AllowWildcard {
		t.Fatalf("new project must have no origins, got %+v", got)
	}
	if got.AllowedOrigins == nil {
		t.Fatalf("list must serialise as [] not null: %s", w.Body.String())
	}
}

func TestCors_PutValidatesEntries(t *testing.T) {
	f := setupCorsHandler(t)
	cases := []struct {
		name  string
		body  interface{}
		wantC int
	}{
		{"absolute origins", corsBody([]string{testCorsOriginA, testCorsOriginB}), http.StatusOK},
		{"empty list clears", corsBody([]string{}), http.StatusOK},
		{"missing list clears", map[string]interface{}{}, http.StatusOK},
		{"no scheme", corsBody([]string{"app.example.com"}), http.StatusBadRequest},
		{"path", corsBody([]string{testCorsOriginA + "/"}), http.StatusBadRequest},
		{"subdomain wildcard", corsBody([]string{"https://*.example.com"}), http.StatusBadRequest},
		{"wildcard without flag", corsBody([]string{"*"}), http.StatusBadRequest},
		{"wildcard mixed", map[string]interface{}{"allowedOrigins": []string{"*", testCorsOriginA}, "allowWildcard": true}, http.StatusBadRequest},
		{"wildcard confirmed", map[string]interface{}{"allowedOrigins": []string{"*"}, "allowWildcard": true}, http.StatusOK},
		{"wrong shape", map[string]string{"allowedOrigins": testCorsOriginA}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := doJSON(f.router, "PUT", testCorsPath, tc.body)
			if w.Code != tc.wantC {
				t.Fatalf("PUT %v: got %d want %d (%s)", tc.body, w.Code, tc.wantC, w.Body.String())
			}
		})
	}
}

func TestCors_PutPersistsCanonicalListAndGetReadsItBack(t *testing.T) {
	f := setupCorsHandler(t)
	w := doJSON(f.router, "PUT", testCorsPath, corsBody([]string{" HTTPS://App.Example.com:443 ", testCorsOriginB, testCorsOriginA}))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	want := []string{testCorsOriginB, testCorsOriginA}
	got := decodeCors(t, w.Body.Bytes())
	if !reflect.DeepEqual(got.AllowedOrigins, want) || got.AllowWildcard {
		t.Fatalf("PUT response = %+v want %v", got, want)
	}
	if !reflect.DeepEqual(f.cors.origins[testCorsProject], want) {
		t.Fatalf("store = %v want %v", f.cors.origins[testCorsProject], want)
	}
	w = doJSON(f.router, "GET", testCorsPath, nil)
	if got := decodeCors(t, w.Body.Bytes()); !reflect.DeepEqual(got.AllowedOrigins, want) {
		t.Fatalf("GET = %v want %v", got.AllowedOrigins, want)
	}
}

func TestCors_WildcardRoundTripReportsFlag(t *testing.T) {
	f := setupCorsHandler(t)
	w := doJSON(f.router, "PUT", testCorsPath, map[string]interface{}{"allowedOrigins": []string{"*"}, "allowWildcard": true})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	w = doJSON(f.router, "GET", testCorsPath, nil)
	got := decodeCors(t, w.Body.Bytes())
	if !got.AllowWildcard || !reflect.DeepEqual(got.AllowedOrigins, []string{"*"}) {
		t.Fatalf("GET after wildcard PUT = %+v", got)
	}
}

func TestCors_RejectedPutLeavesStoreUntouched(t *testing.T) {
	f := setupCorsHandler(t)
	f.cors.origins[testCorsProject] = []string{testCorsOriginA}
	if w := doJSON(f.router, "PUT", testCorsPath, corsBody([]string{testCorsOriginA, "nope"})); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	if got := f.cors.origins[testCorsProject]; !reflect.DeepEqual(got, []string{testCorsOriginA}) {
		t.Fatalf("store changed on a rejected PUT: %v", got)
	}
}

func TestCors_InvalidProjectIDIsRejected(t *testing.T) {
	f := setupCorsHandler(t)
	if w := doJSON(f.router, "GET", "/api/projects/bad..id/cors", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("GET bad id: %d", w.Code)
	}
	if w := doJSON(f.router, "PUT", "/api/projects/bad..id/cors", corsBody(nil)); w.Code != http.StatusBadRequest {
		t.Fatalf("PUT bad id: %d", w.Code)
	}
}

func TestCors_StoreErrorIs500(t *testing.T) {
	f := setupCorsHandler(t)
	f.cors.err = errors.New("pq: connection reset")
	if w := doJSON(f.router, "GET", testCorsPath, nil); w.Code != http.StatusInternalServerError {
		t.Fatalf("GET with failing store: %d", w.Code)
	}
	if w := doJSON(f.router, "PUT", testCorsPath, corsBody([]string{testCorsOriginA})); w.Code != http.StatusInternalServerError {
		t.Fatalf("PUT with failing store: %d", w.Code)
	}
}

// setupCorsInfoRouter mounts /info on a handler whose org store resolves the
// project's org, so the CORS branch of GetProjectInfo is reachable.
func setupCorsInfoRouter(t *testing.T) (*chi.Mux, *ProvisioningHandler, *memCorsStore) {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	if err := store.Save(&domain.DatabaseInstance{ProjectID: testCorsProject, ProjectName: "web", OrgID: "org-cors", Status: "ACTIVE"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	svc := service.NewProvisioningService(store, provisioner.NewFactory(), nil)
	h := NewProvisioningHandler(svc, &adminOrgStore{org: &domain.Org{ID: "org-cors", Slug: "cors", Name: "Cors"}})
	cors := &memCorsStore{origins: map[string][]string{}}
	h.SetCorsStore(cors)
	r := chi.NewRouter()
	r.Get("/api/projects/{projectId}/info", h.GetProjectInfo)
	return r, h, cors
}

func TestCors_InfoExposesOriginsAndNeverNull(t *testing.T) {
	r, h, cors := setupCorsInfoRouter(t)
	cors.origins[testCorsProject] = []string{testCorsOriginA}
	w := doJSON(r, "GET", "/api/projects/"+testCorsProject+"/info", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("info: %d %s", w.Code, w.Body.String())
	}
	var info domain.ProjectInfo
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(info.CorsAllowedOrigins, []string{testCorsOriginA}) {
		t.Fatalf("corsAllowedOrigins = %v", info.CorsAllowedOrigins)
	}

	h.SetCorsStore(nil)
	w = doJSON(r, "GET", "/api/projects/"+testCorsProject+"/info", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"corsAllowedOrigins":[]`) {
		t.Fatalf("info without a cors store must report an empty list: %d %s", w.Code, w.Body.String())
	}
}

func TestCors_InfoIs503WhenAllowlistUnreadable(t *testing.T) {
	r, _, cors := setupCorsInfoRouter(t)
	cors.err = errors.New("pq: connection reset")
	w := doJSON(r, "GET", "/api/projects/"+testCorsProject+"/info", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("info with failing cors store must be 503 (data plane keeps last-good), got %d", w.Code)
	}
}

func TestCors_WithoutStoreIsUnavailable(t *testing.T) {
	f := setupCorsHandler(t)
	f.handler.SetCorsStore(nil)
	if w := doJSON(f.router, "GET", testCorsPath, nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET without store: %d", w.Code)
	}
	if w := doJSON(f.router, "PUT", testCorsPath, corsBody([]string{testCorsOriginA})); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("PUT without store: %d", w.Code)
	}
}
