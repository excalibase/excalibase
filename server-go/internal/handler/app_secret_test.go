package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
)

const (
	secretTestApp   = "app-1"
	secretTestValue = "sk_live_write_only_value"
	secretsBasePath = "/api/projects/" + appTestProject + "/apps/"
)

type failingPutVault struct{ *fakeVault }

func (failingPutVault) Put(string, map[string]string) error {
	return errors.New("vault at 10.0.0.9:8200 refused")
}

type conflictingAppStore struct{ *fakeAppStore }

func (conflictingAppStore) Update(*apphost.App, int) error { return apphost.ErrAppVersionConflict }

type failingUpdateStore struct{ *fakeAppStore }

func (failingUpdateStore) Update(*apphost.App, int) error { return errPersisted }

type vanishingAppStore struct{ *fakeAppStore }

func (vanishingAppStore) Update(*apphost.App, int) error { return apphost.ErrAppNotFound }

func seedSecretApp(t *testing.T, store *fakeAppStore) {
	t.Helper()
	value := "production"
	app := &apphost.App{
		ID: secretTestApp, ProjectID: appTestProject, Name: "storefront",
		Image: "ghcr.io/acme/storefront:1.4.2", Port: 8080, Replicas: 1,
		Tier: domain.Standard, Status: apphost.StatusCreated,
		Env: []apphost.EnvVar{{Name: "MODE", Kind: apphost.KindLiteral, Value: &value}},
	}
	if err := store.Create(app); err != nil {
		t.Fatalf("seed app: %v", err)
	}
}

func appSurfaceRouter(store apphost.Store, secrets *AppSecretHandler, deployer *fakeAppDeployer) chi.Router {
	apps := NewAppHandler(store, newFakeSources("storefront_db"), testAppRoute)
	deploys := NewAppDeployHandler(deployer)
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/apps", func(r chi.Router) {
		r.Get("/", apps.List)
		r.Route("/{appId}", func(r chi.Router) {
			r.Get("/", apps.Get)
			r.Get("/deploys", deploys.ListDeploys)
			r.Put("/secrets/{name}", secrets.Set)
		})
	})
	return r
}

func putSecret(r chi.Router, appID, name, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, secretsBasePath+appID+"/secrets/"+name, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func valueBody(value string) string {
	raw, _ := json.Marshal(map[string]string{"value": value})
	return string(raw)
}

func TestAppSecret_SetStoresTheValueAndPointsTheVariableAtIt(t *testing.T) {
	store, vault := newFakeAppStore(), newFakeVault()
	seedSecretApp(t, store)
	r := appSurfaceRouter(store, NewAppSecretHandler(store, vault), newFakeAppDeployer())

	w := putSecret(r, secretTestApp, "API_KEY", valueBody(secretTestValue))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["name"] != "API_KEY" || body["set"] != true {
		t.Fatalf("body = %v, want name and set only", body)
	}
	if w.Header().Get("ETag") != "2" {
		t.Fatalf("ETag = %q, want the app's new version 2", w.Header().Get("ETag"))
	}

	ref := apphost.AppSecretRef(appTestProject, secretTestApp, "API_KEY")
	if got := vault.data[ref.Path][ref.Key]; got != secretTestValue {
		t.Fatalf("vault holds %q at %s, want the value", got, ref.Path)
	}
	app, _ := store.Get(appTestProject, secretTestApp)
	last := app.Env[len(app.Env)-1]
	if last.Name != "API_KEY" || last.Kind != apphost.KindSecret || *last.Secret != ref || last.Value != nil {
		t.Fatalf("env var = %+v, want a pointer to %v and no value", last, ref)
	}
}

func TestAppSecret_ReplacingAValueOverwritesIt(t *testing.T) {
	store, vault := newFakeAppStore(), newFakeVault()
	seedSecretApp(t, store)
	r := appSurfaceRouter(store, NewAppSecretHandler(store, vault), newFakeAppDeployer())

	putSecret(r, secretTestApp, "MODE", valueBody("first"))
	if w := putSecret(r, secretTestApp, "MODE", valueBody("second")); w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	ref := apphost.AppSecretRef(appTestProject, secretTestApp, "MODE")
	if got := vault.data[ref.Path][ref.Key]; got != "second" {
		t.Fatalf("vault holds %q, want the replacement", got)
	}
	app, _ := store.Get(appTestProject, secretTestApp)
	if len(app.Env) != 1 || app.Env[0].Kind != apphost.KindSecret {
		t.Fatalf("env = %+v, want MODE turned into one secret", app.Env)
	}
}

func TestAppSecret_Refusals(t *testing.T) {
	cases := []struct {
		name, appID, varName, body string
		want                       int
	}{
		{"unknown app", "app-404", "API_KEY", valueBody("x"), http.StatusNotFound},
		{"invalid app id", "bad%20id", "API_KEY", valueBody("x"), http.StatusBadRequest},
		{"invalid variable name", secretTestApp, "1BAD", valueBody("x"), http.StatusBadRequest},
		{"empty value", secretTestApp, "API_KEY", valueBody(""), http.StatusBadRequest},
		{"value too long", secretTestApp, "API_KEY", valueBody(strings.Repeat("x", apphost.MaxLiteralValueLength+1)), http.StatusBadRequest},
		{"missing value", secretTestApp, "API_KEY", `{}`, http.StatusBadRequest},
		{"not json", secretTestApp, "API_KEY", `nope`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, vault := newFakeAppStore(), newFakeVault()
			seedSecretApp(t, store)
			r := appSurfaceRouter(store, NewAppSecretHandler(store, vault), newFakeAppDeployer())
			w := putSecret(r, tc.appID, tc.varName, tc.body)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
			if len(vault.data) != 0 {
				t.Fatalf("a refused request wrote to the vault: %v", vault.data)
			}
		})
	}
}

func TestAppSecret_NoVaultIsUnavailable(t *testing.T) {
	store := newFakeAppStore()
	seedSecretApp(t, store)
	r := appSurfaceRouter(store, NewAppSecretHandler(store, nil), newFakeAppDeployer())
	if w := putSecret(r, secretTestApp, "API_KEY", valueBody("x")); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", w.Code)
	}
}

func TestAppSecret_VaultFailureLeavesTheAppAlone(t *testing.T) {
	store := newFakeAppStore()
	seedSecretApp(t, store)
	r := appSurfaceRouter(store, NewAppSecretHandler(store, failingPutVault{newFakeVault()}), newFakeAppDeployer())
	w := putSecret(r, secretTestApp, "API_KEY", valueBody("x"))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", w.Code)
	}
	if strings.Contains(w.Body.String(), "10.0.0.9") {
		t.Fatalf("response leaked vault detail: %s", w.Body.String())
	}
	app, _ := store.Get(appTestProject, secretTestApp)
	if len(app.Env) != 1 || app.Version != 1 {
		t.Fatalf("app changed after a failed vault write: %+v", app)
	}
}

func TestAppSecret_StoreFailures(t *testing.T) {
	cases := []struct {
		name  string
		store func(*fakeAppStore) apphost.Store
		want  int
	}{
		{"concurrent edit", func(f *fakeAppStore) apphost.Store { return conflictingAppStore{f} }, http.StatusPreconditionFailed},
		{"read failure", func(f *fakeAppStore) apphost.Store { f.failWith = errPersisted; return f }, http.StatusInternalServerError},
		{"write failure", func(f *fakeAppStore) apphost.Store { return failingUpdateStore{f} }, http.StatusInternalServerError},
		{"app deleted meanwhile", func(f *fakeAppStore) apphost.Store { return vanishingAppStore{f} }, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeAppStore()
			seedSecretApp(t, fake)
			store := tc.store(fake)
			r := appSurfaceRouter(store, NewAppSecretHandler(store, newFakeVault()), newFakeAppDeployer())
			w := putSecret(r, secretTestApp, "API_KEY", valueBody("x"))
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "10.0.0.1") {
				t.Fatalf("response leaked storage detail: %s", w.Body.String())
			}
		})
	}
}

// Once written, a secret value is reachable only by the deploy that reads the
// vault: no response on the app surface carries it back.
func TestAppSecret_NoResponseEverCarriesTheValue(t *testing.T) {
	store, vault := newFakeAppStore(), newFakeVault()
	seedSecretApp(t, store)
	deployer := newFakeAppDeployer()
	r := appSurfaceRouter(store, NewAppSecretHandler(store, vault), deployer)

	put := putSecret(r, secretTestApp, "API_KEY", valueBody(secretTestValue))
	if put.Code != http.StatusOK {
		t.Fatalf("set: status %d", put.Code)
	}
	app, _ := store.Get(appTestProject, secretTestApp)
	deploy := sampleMaskedDeploy("dep-1")
	deploy.Config = apphost.ConfigFromApp(app)
	deployer.listDeploys = []*apphost.Deploy{deploy}

	bodies := map[string]string{"PUT secret": put.Body.String()}
	for _, path := range []string{
		secretsBasePath,
		secretsBasePath + secretTestApp + "/",
		secretsBasePath + secretTestApp + "/deploys",
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d", path, w.Code)
		}
		bodies["GET "+path] = w.Body.String()
	}
	for route, body := range bodies {
		if strings.Contains(body, secretTestValue) {
			t.Fatalf("%s returned the secret value: %s", route, body)
		}
	}
}
