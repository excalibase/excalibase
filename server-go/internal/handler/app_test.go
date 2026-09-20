package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

const appTestProject = "proj_apps1"

// errPersisted stands in for a storage failure carrying detail a caller must
// never be shown.
var errPersisted = errors.New("pq: connection refused: 10.0.0.1:5432")

// fakeAppStore is an in-memory apphost.Store. It keeps the handler tests free
// of Docker while still exercising the real routing, validation and
// serialization paths. The one-app rule is enforced here as the Postgres store
// enforces it, so the handler's conflict path is exercised for real.
type fakeAppStore struct {
	mu       sync.Mutex
	apps     map[string]apphost.App // keyed by projectID + "/" + id
	failWith error
}

func newFakeAppStore() *fakeAppStore {
	return &fakeAppStore{apps: map[string]apphost.App{}}
}

func appKey(projectID, id string) string { return projectID + "/" + id }

func (f *fakeAppStore) Create(app *apphost.App) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return f.failWith
	}
	if err := app.Validate(); err != nil {
		return err
	}
	held := 0
	for _, existing := range f.apps {
		if existing.ProjectID != app.ProjectID {
			continue
		}
		held++
		if existing.Name == app.Name {
			return apphost.ErrAppNameTaken
		}
	}
	if held >= apphost.MaxAppsPerProject {
		return apphost.ErrAppLimitReached
	}
	app.Version = 1
	f.apps[appKey(app.ProjectID, app.ID)] = *app
	return nil
}

func (f *fakeAppStore) Get(projectID, id string) (*apphost.App, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return nil, f.failWith
	}
	app, ok := f.apps[appKey(projectID, id)]
	if !ok {
		return nil, nil
	}
	copied := app
	return &copied, nil
}

func (f *fakeAppStore) List(projectID string) ([]*apphost.App, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return nil, f.failWith
	}
	out := []*apphost.App{}
	for _, app := range f.apps {
		if app.ProjectID == projectID {
			copied := app
			out = append(out, &copied)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *fakeAppStore) Update(app *apphost.App) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return f.failWith
	}
	if err := app.Validate(); err != nil {
		return err
	}
	existing, ok := f.apps[appKey(app.ProjectID, app.ID)]
	if !ok {
		return apphost.ErrAppNotFound
	}
	app.Version = existing.Version + 1
	f.apps[appKey(app.ProjectID, app.ID)] = *app
	return nil
}

func (f *fakeAppStore) Delete(projectID, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return f.failWith
	}
	if _, ok := f.apps[appKey(projectID, id)]; !ok {
		return apphost.ErrAppNotFound
	}
	delete(f.apps, appKey(projectID, id))
	return nil
}

// fakeSources is an apphost.SourceLookup over a known set of sources, so the
// handler tests exercise the real refusal path without a database.
type fakeSources struct {
	names map[string]bool
	err   error
}

func newFakeSources(names ...string) *fakeSources {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return &fakeSources{names: set}
}

func (f *fakeSources) HasSource(_ string, kind apphost.SourceKind, name string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return kind == apphost.SourceDatabase && f.names[name], nil
}

func setupAppRouter(t *testing.T) (chi.Router, *fakeAppStore) {
	r, store, _ := setupAppRouterWithSources(t, newFakeSources("storefront_db"))
	return r, store
}

func setupAppRouterWithSources(t *testing.T, sources *fakeSources) (chi.Router, *fakeAppStore, *fakeSources) {
	t.Helper()
	store := newFakeAppStore()
	h := NewAppHandler(store, sources)
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/apps", func(r chi.Router) { h.Routes(r) })
	return r, store, sources
}

func doAppRequest(t *testing.T, r chi.Router, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, path, nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		req = httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func validAppBody() map[string]any {
	return map[string]any{
		"name":            "storefront",
		"image":           "ghcr.io/acme/storefront:1.4.2",
		"port":            8080,
		"replicas":        1,
		"tier":            string(domain.Standard),
		"healthCheckPath": "/healthz",
		"env": []map[string]any{
			{"name": "MODE", "kind": "literal", "value": "production"},
		},
	}
}

func decodeApp(t *testing.T, w *httptest.ResponseRecorder) apphost.App {
	t.Helper()
	var app apphost.App
	if err := json.Unmarshal(w.Body.Bytes(), &app); err != nil {
		t.Fatalf("decode app: %v (body=%s)", err, w.Body.String())
	}
	return app
}

func createAppForTest(t *testing.T, r chi.Router) apphost.App {
	t.Helper()
	w := doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/", validAppBody())
	if w.Code != http.StatusCreated {
		t.Fatalf("create: got %d, body=%s", w.Code, w.Body.String())
	}
	return decodeApp(t, w)
}

func TestAppCreateStoresTheRecord(t *testing.T) {
	r, store := setupAppRouter(t)
	app := createAppForTest(t, r)

	if app.ID == "" {
		t.Error("the server must assign the app id")
	}
	if app.ProjectID != appTestProject {
		t.Errorf("project must come from the path, got %q", app.ProjectID)
	}
	if app.Image != "ghcr.io/acme/storefront:1.4.2" {
		t.Errorf("the image must be stored exactly as given, got %q", app.Image)
	}
	if app.ResolvedDigest != "" {
		t.Error("nothing may resolve a digest at create time")
	}
	if app.Status != apphost.StatusCreated {
		t.Errorf("status: got %q want %q", app.Status, apphost.StatusCreated)
	}
	if _, ok := store.apps[appKey(appTestProject, app.ID)]; !ok {
		t.Error("the app must reach the store")
	}
}

// The project in the path is authoritative: a body naming another project
// cannot plant a record there.
func TestAppCreateIgnoresProjectInBody(t *testing.T) {
	r, _ := setupAppRouter(t)
	body := validAppBody()
	body["projectId"] = "proj_victim"
	w := doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: got %d, body=%s", w.Code, w.Body.String())
	}
	if got := decodeApp(t, w); got.ProjectID != appTestProject {
		t.Errorf("project must come from the path, got %q", got.ProjectID)
	}
}

// A caller cannot choose the status: it is derived from what was asked for.
func TestAppCreateIgnoresStatusInBody(t *testing.T) {
	r, _ := setupAppRouter(t)
	body := validAppBody()
	body["status"] = apphost.StatusRunning
	w := doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: got %d, body=%s", w.Code, w.Body.String())
	}
	if got := decodeApp(t, w); got.Status != apphost.StatusCreated {
		t.Errorf("a caller must not be able to claim a running app, got %q", got.Status)
	}
}

// Zero replicas is a legal request and records an intentionally stopped app.
func TestAppCreateWithZeroReplicasIsStopped(t *testing.T) {
	r, _ := setupAppRouter(t)
	body := validAppBody()
	body["replicas"] = 0
	w := doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: got %d, body=%s", w.Code, w.Body.String())
	}
	got := decodeApp(t, w)
	if got.Replicas != 0 || got.Status != apphost.StatusStopped {
		t.Errorf("zero replicas must record a stopped app, got replicas=%d status=%q", got.Replicas, got.Status)
	}
}

func TestAppCreateRejectsBadRequests(t *testing.T) {
	cases := map[string]func(map[string]any){
		"missing name":        func(b map[string]any) { delete(b, "name") },
		"bad name":            func(b map[string]any) { b["name"] = "Store Front" },
		"missing image":       func(b map[string]any) { delete(b, "image") },
		"untagged image":      func(b map[string]any) { b["image"] = "nginx" },
		"missing port":        func(b map[string]any) { delete(b, "port") },
		"port out of range":   func(b map[string]any) { b["port"] = 70000 },
		"missing replicas":    func(b map[string]any) { delete(b, "replicas") },
		"replicas too many":   func(b map[string]any) { b["replicas"] = 9 },
		"missing tier":        func(b map[string]any) { delete(b, "tier") },
		"unknown tier":        func(b map[string]any) { b["tier"] = "PLATINUM" },
		"relative health":     func(b map[string]any) { b["healthCheckPath"] = "healthz" },
		"bad env name":        func(b map[string]any) { b["env"] = []map[string]any{{"name": "a b", "kind": "literal", "value": "x"}} },
		"env with no kind":    func(b map[string]any) { b["env"] = []map[string]any{{"name": "TOKEN", "value": "x"}} },
		"env with no payload": func(b map[string]any) { b["env"] = []map[string]any{{"name": "TOKEN", "kind": "literal"}} },
		"env with two payloads": func(b map[string]any) {
			b["env"] = []map[string]any{{
				"name":   "TOKEN",
				"kind":   "literal",
				"value":  "literal",
				"secret": map[string]any{"path": "projects/" + appTestProject + "/apps/x/secrets", "key": "token"},
			}}
		},
		"env with mismatched kind": func(b map[string]any) {
			b["env"] = []map[string]any{{"name": "TOKEN", "kind": "secret", "value": "x"}}
		},
		"foreign secret path": func(b map[string]any) {
			b["env"] = []map[string]any{{
				"name":   "TOKEN",
				"kind":   "secret",
				"secret": map[string]any{"path": "projects/proj_other/secrets", "key": "token"},
			}}
		},
		"reference to an unknown variable": func(b map[string]any) {
			b["env"] = []map[string]any{{
				"name": "DB", "kind": "reference",
				"reference": map[string]any{"sourceKind": "database", "sourceName": "storefront_db", "variable": "PGSECRET"},
			}}
		},
		"reference to an unknown source kind": func(b map[string]any) {
			b["env"] = []map[string]any{{
				"name": "DB", "kind": "reference",
				"reference": map[string]any{"sourceKind": "cache", "sourceName": "storefront_db", "variable": "PGHOST"},
			}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r, _ := setupAppRouter(t)
			body := validAppBody()
			mutate(body)
			w := doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/", body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want 400, body=%s", w.Code, w.Body.String())
			}
		})
	}
}

// referenceBody is a create body whose only variable references a database.
func referenceBody(sourceName string) map[string]any {
	body := validAppBody()
	body["env"] = []map[string]any{{
		"name": "DATABASE_URL", "kind": "reference",
		"reference": map[string]any{
			"sourceKind": "database", "sourceName": sourceName, "variable": "DATABASE_URL",
		},
	}}
	return body
}

// A reference to a source the project has is stored as a reference: nothing is
// resolved, and no value is invented.
func TestAppCreateAcceptsAResolvableReference(t *testing.T) {
	r, _ := setupAppRouter(t)
	w := doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/", referenceBody("storefront_db"))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: got %d, body=%s", w.Code, w.Body.String())
	}
	got := decodeApp(t, w)
	if got.Env[0].Kind != apphost.KindReference || got.Env[0].Value != nil {
		t.Fatalf("a reference must be stored as a reference, got %+v", got.Env[0])
	}
	if got.Env[0].Reference.SourceName != "storefront_db" {
		t.Errorf("the target must be stored structurally, got %+v", got.Env[0].Reference)
	}
	resolutions := got.Resolutions()
	if len(resolutions) != 1 || resolutions[0].Scope != apphost.ScopeInternal {
		t.Errorf("the reference must resolve to the internal address, got %+v", resolutions)
	}
}

// An unresolvable reference is fatal: the app is not created, and the refusal
// names the variable and what it pointed at.
func TestAppCreateRefusesAnUnresolvableReference(t *testing.T) {
	r, store, _ := setupAppRouterWithSources(t, newFakeSources("storefront_db"))
	w := doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/", referenceBody("nowhere_db"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400, body=%s", w.Code, w.Body.String())
	}
	for _, want := range []string{"DATABASE_URL", "nowhere_db"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("the refusal must name %q: %s", want, w.Body.String())
		}
	}
	if len(store.apps) != 0 {
		t.Error("nothing may be stored when a reference does not resolve")
	}
}

// A project with no database has nothing to reference, and nothing is injected
// on its behalf — an apps-only project is a normal project.
func TestAppCreateWithoutADatabaseNeedsNoReference(t *testing.T) {
	r, _, _ := setupAppRouterWithSources(t, newFakeSources())
	w := doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/", validAppBody())
	if w.Code != http.StatusCreated {
		t.Fatalf("an apps-only project must be able to create an app: %d %s", w.Code, w.Body.String())
	}
	got := decodeApp(t, w)
	if len(got.Env) != 1 || got.Env[0].Name != "MODE" {
		t.Errorf("no variable may be injected automatically, got %+v", got.Env)
	}

	w = doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/", referenceBody("storefront_db"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("referencing a database the project lacks must be refused, got %d", w.Code)
	}
}

// An update may not introduce an unresolvable reference either.
func TestAppUpdateRefusesAnUnresolvableReference(t *testing.T) {
	r, _ := setupAppRouter(t)
	created := createAppForTest(t, r)
	w := doAppRequest(t, r, http.MethodPatch, "/api/projects/"+appTestProject+"/apps/"+created.ID+"/",
		map[string]any{"env": referenceBody("nowhere_db")["env"]})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400, body=%s", w.Code, w.Body.String())
	}
}

// A lookup that fails is an internal error, not a refusal: "the project has no
// such source" and "we could not find out" are different facts.
func TestAppCreateSourceLookupFailureIs500(t *testing.T) {
	sources := newFakeSources("storefront_db")
	sources.err = errPersisted
	r, _, _ := setupAppRouterWithSources(t, sources)
	w := doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/", referenceBody("storefront_db"))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500, body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "10.0.0.1") {
		t.Errorf("the refusal must not leak storage detail: %s", w.Body.String())
	}
}

// Every cap is refused at the API boundary rather than at deploy time.
func TestAppCreateEnforcesVariableCaps(t *testing.T) {
	oversized := func(count int, value string) []map[string]any {
		out := make([]map[string]any, 0, count)
		for i := 0; i < count; i++ {
			out = append(out, map[string]any{
				"name": fmt.Sprintf("K%d", i), "kind": "literal", "value": value,
			})
		}
		return out
	}
	cases := map[string][]map[string]any{
		"too many variables": oversized(apphost.MaxEnvVars+1, "x"),
		"literal over 8KiB": {{
			"name": "BIG", "kind": "literal", "value": strings.Repeat("v", apphost.MaxLiteralValueLength+1),
		}},
		"name over 128 bytes": {{
			"name": strings.Repeat("N", apphost.MaxEnvNameLength+1), "kind": "literal", "value": "x",
		}},
		"set over 64KiB": oversized(
			apphost.MaxTotalEnvBytes/apphost.MaxLiteralValueLength+1,
			strings.Repeat("v", apphost.MaxLiteralValueLength)),
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			r, _ := setupAppRouter(t)
			body := validAppBody()
			body["env"] = env
			w := doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/", body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want 400", w.Code)
			}
		})
	}
}

func TestAppCreateRejectsMalformedJSON(t *testing.T) {
	r, _ := setupAppRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+appTestProject+"/apps/",
		bytes.NewReader([]byte("{not json")))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", w.Code)
	}
}

// One app per project, reported as a conflict rather than a validation error.
func TestAppCreateSecondAppIsAConflict(t *testing.T) {
	r, _ := setupAppRouter(t)
	createAppForTest(t, r)

	body := validAppBody()
	body["name"] = "second"
	w := doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/", body)
	if w.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409, body=%s", w.Code, w.Body.String())
	}
}

func TestAppGetAndList(t *testing.T) {
	r, _ := setupAppRouter(t)
	created := createAppForTest(t, r)

	w := doAppRequest(t, r, http.MethodGet, "/api/projects/"+appTestProject+"/apps/"+created.ID+"/", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get: got %d, body=%s", w.Code, w.Body.String())
	}
	if got := decodeApp(t, w); got.ID != created.ID {
		t.Errorf("get returned the wrong app: %q", got.ID)
	}

	w = doAppRequest(t, r, http.MethodGet, "/api/projects/"+appTestProject+"/apps/", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list: got %d", w.Code)
	}
	var list []apphost.App
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v (body=%s)", err, w.Body.String())
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Errorf("list must hold the created app, got %+v", list)
	}
}

// A project with no app lists an empty array, never null: a client must not
// have to tell "no apps" from "the field is missing".
func TestAppListEmptyIsAnArray(t *testing.T) {
	r, _ := setupAppRouter(t)
	w := doAppRequest(t, r, http.MethodGet, "/api/projects/proj_empty/apps/", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d", w.Code)
	}
	if body := w.Body.String(); body != "[]\n" {
		t.Errorf("empty list must serialise as [], got %q", body)
	}
}

func TestAppGetMissingIs404(t *testing.T) {
	r, _ := setupAppRouter(t)
	w := doAppRequest(t, r, http.MethodGet, "/api/projects/"+appTestProject+"/apps/app_absent/", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", w.Code)
	}
}

// An app is only reachable through the project that owns it.
func TestAppIsScopedToItsProject(t *testing.T) {
	r, _ := setupAppRouter(t)
	created := createAppForTest(t, r)
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		w := doAppRequest(t, r, method, "/api/projects/proj_intruder/apps/"+created.ID+"/", nil)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s through another project must be 404, got %d", method, w.Code)
		}
	}
}

// Update is partial: an omitted field keeps its stored value.
func TestAppUpdateAppliesOnlyWhatWasSent(t *testing.T) {
	r, _ := setupAppRouter(t)
	created := createAppForTest(t, r)

	w := doAppRequest(t, r, http.MethodPatch, "/api/projects/"+appTestProject+"/apps/"+created.ID+"/",
		map[string]any{"image": "ghcr.io/acme/storefront:1.5.0"})
	if w.Code != http.StatusOK {
		t.Fatalf("update: got %d, body=%s", w.Code, w.Body.String())
	}
	got := decodeApp(t, w)
	if got.Image != "ghcr.io/acme/storefront:1.5.0" {
		t.Errorf("image was not applied, got %q", got.Image)
	}
	if got.Port != created.Port || got.Tier != created.Tier || got.HealthCheckPath != created.HealthCheckPath {
		t.Errorf("omitted fields must keep their stored values, got %+v", got)
	}
	if len(got.Env) != 1 || got.Env[0].Name != "MODE" {
		t.Errorf("an omitted env set must be untouched, got %+v", got.Env)
	}
}

// Sending env replaces the set outright: an emptied value survives and a
// removed key disappears.
func TestAppUpdateReplacesTheEnvSet(t *testing.T) {
	r, _ := setupAppRouter(t)
	created := createAppForTest(t, r)

	w := doAppRequest(t, r, http.MethodPatch, "/api/projects/"+appTestProject+"/apps/"+created.ID+"/",
		map[string]any{"env": []map[string]any{{"name": "MODE", "kind": "literal", "value": ""}}})
	if w.Code != http.StatusOK {
		t.Fatalf("update: got %d, body=%s", w.Code, w.Body.String())
	}
	got := decodeApp(t, w)
	if len(got.Env) != 1 {
		t.Fatalf("env must hold exactly the sent set, got %+v", got.Env)
	}
	if got.Env[0].Value == nil || *got.Env[0].Value != "" {
		t.Errorf("an emptied value must survive as empty, got %v", got.Env[0].Value)
	}

	w = doAppRequest(t, r, http.MethodPatch, "/api/projects/"+appTestProject+"/apps/"+created.ID+"/",
		map[string]any{"env": []map[string]any{}})
	if w.Code != http.StatusOK {
		t.Fatalf("update: got %d, body=%s", w.Code, w.Body.String())
	}
	if got := decodeApp(t, w); len(got.Env) != 0 {
		t.Errorf("an emptied env set must clear every key, got %+v", got.Env)
	}
}

// Swapping a literal value for a secret reference must not leave the literal
// behind: the two are mutually exclusive.
func TestAppUpdateSwapsAValueForASecret(t *testing.T) {
	r, _ := setupAppRouter(t)
	created := createAppForTest(t, r)

	w := doAppRequest(t, r, http.MethodPatch, "/api/projects/"+appTestProject+"/apps/"+created.ID+"/",
		map[string]any{"env": []map[string]any{{
			"name":   "MODE",
			"kind":   "secret",
			"secret": map[string]any{"path": "projects/" + appTestProject + "/apps/x/secrets", "key": "mode"},
		}}})
	if w.Code != http.StatusOK {
		t.Fatalf("update: got %d, body=%s", w.Code, w.Body.String())
	}
	got := decodeApp(t, w)
	if got.Env[0].Value != nil {
		t.Errorf("the literal value must be gone, got %q", *got.Env[0].Value)
	}
	if got.Env[0].Secret == nil || got.Env[0].Secret.Key != "mode" {
		t.Errorf("the secret reference must be applied, got %+v", got.Env[0].Secret)
	}
}

// Replicas moving to zero is a stop, and back is not a claim that it runs.
func TestAppUpdateReplicasMovesTheStatus(t *testing.T) {
	r, _ := setupAppRouter(t)
	created := createAppForTest(t, r)
	path := "/api/projects/" + appTestProject + "/apps/" + created.ID + "/"

	w := doAppRequest(t, r, http.MethodPatch, path, map[string]any{"replicas": 0})
	if w.Code != http.StatusOK {
		t.Fatalf("update: got %d, body=%s", w.Code, w.Body.String())
	}
	if got := decodeApp(t, w); got.Status != apphost.StatusStopped {
		t.Errorf("zero replicas must stop the app, got %q", got.Status)
	}

	w = doAppRequest(t, r, http.MethodPatch, path, map[string]any{"replicas": 2})
	if w.Code != http.StatusOK {
		t.Fatalf("update: got %d, body=%s", w.Code, w.Body.String())
	}
	if got := decodeApp(t, w); got.Status != apphost.StatusCreated {
		t.Errorf("restarting must not claim the app runs, got %q", got.Status)
	}
}

func TestAppUpdateRejectsBadValues(t *testing.T) {
	cases := []map[string]any{
		{"image": "nginx"},
		{"port": 0},
		{"replicas": 4},
		{"tier": "PLATINUM"},
		{"name": "Bad Name"},
		{"healthCheckPath": "healthz"},
		{"env": []map[string]any{{"name": "TOKEN", "kind": "literal"}}},
	}
	for _, body := range cases {
		r, _ := setupAppRouter(t)
		created := createAppForTest(t, r)
		w := doAppRequest(t, r, http.MethodPatch,
			"/api/projects/"+appTestProject+"/apps/"+created.ID+"/", body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%v: got %d, want 400, body=%s", body, w.Code, w.Body.String())
		}
	}
}

func TestAppUpdateMissingIs404(t *testing.T) {
	r, _ := setupAppRouter(t)
	w := doAppRequest(t, r, http.MethodPatch, "/api/projects/"+appTestProject+"/apps/app_absent/",
		map[string]any{"replicas": 2})
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404, body=%s", w.Code, w.Body.String())
	}
}

func TestAppDelete(t *testing.T) {
	r, store := setupAppRouter(t)
	created := createAppForTest(t, r)

	w := doAppRequest(t, r, http.MethodDelete, "/api/projects/"+appTestProject+"/apps/"+created.ID+"/", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: got %d, body=%s", w.Code, w.Body.String())
	}
	if _, ok := store.apps[appKey(appTestProject, created.ID)]; ok {
		t.Error("the app must be gone from the store")
	}
	w = doAppRequest(t, r, http.MethodDelete, "/api/projects/"+appTestProject+"/apps/"+created.ID+"/", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("a repeated delete must be 404, got %d", w.Code)
	}
}

func TestAppRoutesRejectInvalidPathIDs(t *testing.T) {
	r, _ := setupAppRouter(t)
	w := doAppRequest(t, r, http.MethodGet, "/api/projects/"+appTestProject+"/apps/not%20an%20id/", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("an invalid app id must be 400, got %d", w.Code)
	}
}

// A store failure is a 500 that says nothing about the platform's internals.
func TestAppStoreFailureIs500(t *testing.T) {
	r, store := setupAppRouter(t)
	store.failWith = errPersisted
	w := doAppRequest(t, r, http.MethodGet, "/api/projects/"+appTestProject+"/apps/", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500", w.Code)
	}
}

// The only source a project exposes is its own provisioned database, named by
// the database name the platform recorded. A project whose database does not
// exist yet, or is on its way out, exposes nothing — which is what makes a
// reference to it fatal instead of empty.
func TestProjectSourceLookup(t *testing.T) {
	instances := fakestore.NewInstances()
	instances.Items["proj_live"] = &domain.DatabaseInstance{
		ProjectID: "proj_live", DatabaseName: "storefront_db", Status: "ACTIVE",
	}
	instances.Items["proj_unnamed"] = &domain.DatabaseInstance{
		ProjectID: "proj_unnamed", Status: domain.StatusProvisioning,
	}
	instances.Items["proj_going"] = &domain.DatabaseInstance{
		ProjectID: "proj_going", DatabaseName: "storefront_db", Status: string(domain.StatusDeleting),
	}
	lookup := NewProjectSourceLookup(instances)

	cases := []struct {
		name      string
		projectID string
		kind      apphost.SourceKind
		source    string
		want      bool
	}{
		{"the project's database", "proj_live", apphost.SourceDatabase, "storefront_db", true},
		{"another name", "proj_live", apphost.SourceDatabase, "nowhere_db", false},
		{"another kind", "proj_live", "cache", "storefront_db", false},
		{"no database yet", "proj_unnamed", apphost.SourceDatabase, "storefront_db", false},
		{"a database being torn down", "proj_going", apphost.SourceDatabase, "storefront_db", false},
		{"a project with no row at all", "proj_appsonly", apphost.SourceDatabase, "storefront_db", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := lookup.HasSource(tc.projectID, tc.kind, tc.source)
			if err != nil {
				t.Fatalf("HasSource: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// A lookup that cannot read the project reports the failure rather than
// answering "no source", which would turn an outage into a refusal.
func TestProjectSourceLookupPropagatesFailure(t *testing.T) {
	instances := fakestore.NewInstances()
	instances.Err = errPersisted
	if _, err := NewProjectSourceLookup(instances).
		HasSource("proj_live", apphost.SourceDatabase, "storefront_db"); err == nil {
		t.Fatal("a failed read must be reported, not read as an absent source")
	}
}
