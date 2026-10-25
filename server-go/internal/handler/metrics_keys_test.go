package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/promexport"
)

type fakeMetricsKeyStore struct {
	tokens  map[string]*domain.AccessToken
	failAll bool
}

func newFakeMetricsKeyStore(tokens ...*domain.AccessToken) *fakeMetricsKeyStore {
	store := &fakeMetricsKeyStore{tokens: map[string]*domain.AccessToken{}}
	for _, token := range tokens {
		store.tokens[token.TokenHash] = token
	}
	return store
}

func (f *fakeMetricsKeyStore) CreateToken(_ context.Context, token *domain.AccessToken) error {
	if f.failAll {
		return errors.New("pq: down")
	}
	f.tokens[token.TokenHash] = token
	return nil
}

func (f *fakeMetricsKeyStore) FindByTokenHash(_ context.Context, hash string) (*domain.AccessToken, error) {
	return f.tokens[hash], nil
}

func (f *fakeMetricsKeyStore) ListTokensByProject(_ context.Context, projectID string) ([]*domain.AccessToken, error) {
	if f.failAll {
		return nil, errors.New("pq: down")
	}
	var out []*domain.AccessToken
	for _, token := range f.tokens {
		if token.ProjectID == projectID {
			out = append(out, token)
		}
	}
	return out, nil
}

func (f *fakeMetricsKeyStore) DeleteToken(_ context.Context, hash string) error {
	delete(f.tokens, hash)
	return nil
}

type recordedAudit struct{ entries []*domain.AuditEntry }

func (r *recordedAudit) LogAudit(_ context.Context, entry *domain.AuditEntry) error {
	r.entries = append(r.entries, entry)
	return nil
}

func metricsKeysRouter(h *MetricsKeysHandler) http.Handler {
	r := chi.NewRouter()
	passThrough := func(next http.Handler) http.Handler { return next }
	r.Route("/api/projects/{projectId}/metrics-keys", h.Routes(passThrough))
	return r
}

func callMetricsKeys(t *testing.T, h *MetricsKeysHandler, method, target, body string, caller *domain.AccessToken) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	ctx := auth.SetUser(request.Context(), &domain.User{ID: "admin-1", Active: true})
	if caller == nil {
		caller = &domain.AccessToken{Scopes: auth.ScopeSession}
	}
	request = request.WithContext(auth.SetToken(ctx, caller))
	recorder := httptest.NewRecorder()
	metricsKeysRouter(h).ServeHTTP(recorder, request)
	return recorder
}

const keysURL = "/api/projects/p1/metrics-keys"

// EXC-657: a metrics key is a PAT with only metrics:read, bound to the project.
func TestMetricsKeys_CreateShowsTheKeyOnceAndStoresItsHash(t *testing.T) {
	store := newFakeMetricsKeyStore()
	audit := &recordedAudit{}
	h := NewMetricsKeysHandler(store, audit)
	recorder := callMetricsKeys(t, h, http.MethodPost, keysURL, `{"name":"grafana","expiresInDays":30}`, nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", recorder.Code, recorder.Body)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Error("the answer holding the key is never cached")
	}
	var issued struct {
		ID, Name, TokenPrefix, Token string
		ExpiresAt                    time.Time
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	stored := store.tokens[auth.HashToken(issued.Token)]
	if stored == nil || issued.ID != stored.TokenHash {
		t.Fatalf("stored by its hash only: %+v", store.tokens)
	}
	if stored.Scopes != auth.ScopeMetricsRead || stored.ProjectID != "p1" || stored.UserID != "admin-1" || stored.Name != "grafana" {
		t.Errorf("stored: %+v", stored)
	}
	if days := time.Until(issued.ExpiresAt).Hours() / 24; days < 29 || days > 30 {
		t.Errorf("expires in %.1f days", days)
	}
	if len(audit.entries) != 1 || audit.entries[0].Action != "metrics_key.create" || audit.entries[0].ProjectID != "p1" ||
		strings.Contains(audit.entries[0].Details, issued.Token) {
		t.Errorf("audited by prefix: %+v", audit.entries)
	}
}

func TestMetricsKeys_CreateRefusals(t *testing.T) {
	cases := map[string]string{
		"no name":         `{"name":"  "}`,
		"a long name":     `{"name":"` + strings.Repeat("a", 65) + `"}`,
		"a control char":  `{"name":"a\u0007b"}`,
		"too long a life": `{"name":"g","expiresInDays":366}`,
		"no life":         `{"name":"g","expiresInDays":0}`,
		"not json":        `{`,
	}
	for name, body := range cases {
		if recorder := callMetricsKeys(t, NewMetricsKeysHandler(newFakeMetricsKeyStore(), nil), http.MethodPost, keysURL, body, nil); recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, recorder.Code)
		}
	}
}

func TestMetricsKeys_DefaultLifetimeIsNinetyDays(t *testing.T) {
	store := newFakeMetricsKeyStore()
	callMetricsKeys(t, NewMetricsKeysHandler(store, nil), http.MethodPost, keysURL, `{"name":"g"}`, nil)
	for _, token := range store.tokens {
		if days := time.Until(*token.ExpiresAt).Hours() / 24; days < 89 || days > 90 {
			t.Errorf("expires in %.1f days", days)
		}
	}
}

func TestMetricsKeys_NeverOutliveTheTokenThatMadeThem(t *testing.T) {
	store := newFakeMetricsKeyStore()
	soon := time.Now().Add(48 * time.Hour)
	caller := &domain.AccessToken{Scopes: auth.ScopeWrite, ExpiresAt: &soon}
	callMetricsKeys(t, NewMetricsKeysHandler(store, nil), http.MethodPost, keysURL, `{"name":"g","expiresInDays":365}`, caller)
	for _, token := range store.tokens {
		if token.ExpiresAt.After(soon) {
			t.Errorf("expires %v, after its maker %v", token.ExpiresAt, soon)
		}
	}
}

func TestMetricsKeys_AProjectHoldsAtMostTen(t *testing.T) {
	store := newFakeMetricsKeyStore()
	h := NewMetricsKeysHandler(store, nil)
	for i := 0; i < maxMetricsKeysPerProject; i++ {
		if recorder := callMetricsKeys(t, h, http.MethodPost, keysURL, `{"name":"g"}`, nil); recorder.Code != http.StatusCreated {
			t.Fatalf("key %d: %d", i, recorder.Code)
		}
	}
	if recorder := callMetricsKeys(t, h, http.MethodPost, keysURL, `{"name":"g"}`, nil); recorder.Code != http.StatusConflict {
		t.Errorf("the eleventh: %d", recorder.Code)
	}
}

func TestMetricsKeys_ListShowsOnlyThisProjectsMetricsKeys(t *testing.T) {
	expires := time.Now().Add(time.Hour)
	store := newFakeMetricsKeyStore(
		&domain.AccessToken{TokenHash: "h1", TokenPrefix: "excb_1", Name: "grafana", Scopes: auth.ScopeMetricsRead, ProjectID: "p1", ExpiresAt: &expires},
		&domain.AccessToken{TokenHash: "h2", TokenPrefix: "excb_2", Name: "ci", Scopes: auth.ScopeWrite, ProjectID: "p1"},
		&domain.AccessToken{TokenHash: "h3", TokenPrefix: "excb_3", Name: "other", Scopes: auth.ScopeMetricsRead, ProjectID: "p2"},
	)
	recorder := callMetricsKeys(t, NewMetricsKeysHandler(store, nil), http.MethodGet, keysURL, "", nil)
	var keys []map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &keys); err != nil {
		t.Fatalf("%v: %s", err, recorder.Body)
	}
	if len(keys) != 1 || keys[0]["id"] != "h1" || keys[0]["name"] != "grafana" || keys[0]["tokenPrefix"] != "excb_1" {
		t.Errorf("keys: %+v", keys)
	}
	if _, leaked := keys[0]["token"]; leaked {
		t.Error("the list never holds a key")
	}
}

func TestMetricsKeys_RevokeOnlyThisProjectsMetricsKey(t *testing.T) {
	store := newFakeMetricsKeyStore(
		&domain.AccessToken{TokenHash: "h1", TokenPrefix: "excb_1", Scopes: auth.ScopeMetricsRead, ProjectID: "p1"},
		&domain.AccessToken{TokenHash: "h2", Scopes: auth.ScopeWrite, ProjectID: "p1"},
		&domain.AccessToken{TokenHash: "h3", Scopes: auth.ScopeMetricsRead, ProjectID: "p2"},
	)
	audit := &recordedAudit{}
	h := NewMetricsKeysHandler(store, audit)
	for _, id := range []string{"h2", "h3", "missing"} {
		if recorder := callMetricsKeys(t, h, http.MethodDelete, keysURL+"/"+id, "", nil); recorder.Code != http.StatusNotFound {
			t.Errorf("%s: %d", id, recorder.Code)
		}
	}
	if recorder := callMetricsKeys(t, h, http.MethodDelete, keysURL+"/h1", "", nil); recorder.Code != http.StatusNoContent {
		t.Errorf("revoke: %d", recorder.Code)
	}
	if _, kept := store.tokens["h1"]; kept || len(store.tokens) != 2 {
		t.Errorf("only h1 is gone: %v", store.tokens)
	}
	if len(audit.entries) != 1 || audit.entries[0].Action != "metrics_key.revoke" {
		t.Errorf("audit: %+v", audit.entries)
	}
}

func TestMetricsKeys_AStoreFailureSaysNothingInternal(t *testing.T) {
	store := newFakeMetricsKeyStore()
	store.failAll = true
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		recorder := callMetricsKeys(t, NewMetricsKeysHandler(store, nil), method, keysURL, `{"name":"g"}`, nil)
		if recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), "pq") {
			t.Errorf("%s: %d %s", method, recorder.Code, recorder.Body)
		}
	}
}

type fakeSnapshots struct {
	snapshot promexport.Snapshot
	err      error
	asked    string
}

func (f *fakeSnapshots) Snapshot(_ context.Context, projectID string) (promexport.Snapshot, error) {
	f.asked = projectID
	snapshot := f.snapshot
	snapshot.ProjectID = projectID
	return snapshot, f.err
}

func serveMetricsEndpoint(source *fakeSnapshots) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Get("/api/projects/{projectId}/metrics", NewMetricsEndpointHandler(source).Serve)
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/projects/p1/metrics", nil))
	return recorder
}

func TestMetricsEndpoint_AnswersThePathsProjectInTextFormat(t *testing.T) {
	source := &fakeSnapshots{snapshot: promexport.Snapshot{Now: time.Now(), Requests: []domain.RequestTotalsRow{
		{ProjectID: "p1", Family: "graphql", Requests: 2}}}}
	recorder := serveMetricsEndpoint(source)
	if recorder.Code != http.StatusOK || source.asked != "p1" {
		t.Fatalf("%d, asked %q", recorder.Code, source.asked)
	}
	if recorder.Header().Get("Content-Type") != promexport.ContentType || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers: %v", recorder.Header())
	}
	if !strings.Contains(recorder.Body.String(), `excalibase_requests_total{project="p1",surface="graphql",status="ok"} 2`) {
		t.Errorf("body:\n%s", recorder.Body)
	}
}

func TestMetricsEndpoint_AReadFailureSaysNothingInternal(t *testing.T) {
	recorder := serveMetricsEndpoint(&fakeSnapshots{err: errors.New("read request totals: pq: connection refused to 10.0.0.5")})
	if recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), "10.0.0.5") {
		t.Errorf("%d %s", recorder.Code, recorder.Body)
	}
}
