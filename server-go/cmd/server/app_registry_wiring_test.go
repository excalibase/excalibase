package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/appregistry"
	"github.com/excalibase/provisioning-poc/internal/testutil/appregistrytest"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

func registryTokenRouter(t *testing.T, on ...features.Feature) (http.Handler, *appregistry.Internal) {
	t.Helper()
	internal := appregistrytest.New(t)
	cfg := config.AppConfig{DeploymentMode: "selfhosted", AppHostingEnabled: true}
	router, _, _ := buildMatrixWith(t, cfg, func(deps *handlerDeps) {
		deps.features = features.NewStatic(on...)
		token, err := appregistry.NewTokenHandler(internal, func(context.Context, string) (bool, error) { return true, nil })
		if err != nil {
			t.Fatal(err)
		}
		deps.registryToken = token
	})
	return router, internal
}

func askRegistryToken(handler http.Handler, internal *appregistry.Internal) int {
	req := httptest.NewRequest(http.MethodGet, registryTokenPath+"?service="+appregistrytest.Host+"&scope=repository:proj-a/api:pull", nil)
	cred, _ := internal.PullCredential("proj-a")
	req.SetBasicAuth(cred.Username, cred.Password)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code
}

// EXC-540: zot reaches the token service in-cluster; the edge never does, and nothing answers while app git builds are off.
func TestRegistryTokenAnswersOnTheInternalListenerOnly(t *testing.T) {
	router, internal := registryTokenRouter(t, features.AppGitBuilds)
	public, inner := newListeners(config.AppConfig{}, router)
	if code := askRegistryToken(public.Handler, internal); code != http.StatusNotFound {
		t.Errorf("public listener: %d", code)
	}
	if code := askRegistryToken(inner.Handler, internal); code != http.StatusOK {
		t.Errorf("internal listener: %d", code)
	}
	dark, internal := registryTokenRouter(t)
	_, darkInner := newListeners(config.AppConfig{}, dark)
	if code := askRegistryToken(darkInner.Handler, internal); code != http.StatusNotFound {
		t.Errorf("with app git builds off: %d", code)
	}
}

func TestNewInternalRegistry_OnlyWithTheFlagAndAHost(t *testing.T) {
	key := appregistrytest.KeyPEM(t)
	on := config.AppConfig{DarkFeatures: []features.Feature{features.AppGitBuilds},
		AppRegistry: config.AppRegistryConfig{Host: appregistrytest.Host, TokenKeyPEM: key}}
	secrets := memVault{}
	internal, err := newInternalRegistry(on, secrets)
	if err != nil || !internal.CanIssue() || internal.Host() != appregistrytest.Host {
		t.Fatalf("on: %v %v", internal, err)
	}
	if _, err := newInternalRegistry(on, nil); err == nil {
		t.Error("issuing with no vault for the pull generations")
	}
	off := on
	off.DarkFeatures = nil
	guard, err := newInternalRegistry(off, secrets)
	if err != nil || guard == nil || guard.CanIssue() || !guard.Names(appregistrytest.Host+"/proj-a/api:1") {
		t.Errorf("flag off still guards the name, issuing nothing: %v %v", guard, err)
	}
	if newRegistryTokenHandler(guard, nil) != nil || newInternalRegistryHandler(guard, secrets) != nil {
		t.Error("flag off serves no token service and no rotation")
	}
	if newRegistryTokenHandler(internal, fakestore.NewInstances()) == nil || newInternalRegistryHandler(internal, secrets) == nil {
		t.Error("flag on serves the token service and the rotation")
	}
	if newInternalRegistryHandler(internal, nil) != nil || vaultSecrets(nil) != nil {
		t.Error("no vault, no rotation")
	}
	if none, err := newInternalRegistry(config.AppConfig{}, secrets); none != nil || err != nil {
		t.Errorf("no host: %v %v", none, err)
	}
	bad := on
	bad.AppRegistry.TokenKeyPEM = []byte("not a key")
	if _, err := newInternalRegistry(bad, secrets); err == nil {
		t.Error("a key that cannot sign must stop the boot")
	}
}

// The derived password opens nothing once the project is gone or being deleted.
func TestProjectExists_OnlyALiveProject(t *testing.T) {
	instances := fakestore.NewInstances()
	instances.Items["proj-live"] = &domain.DatabaseInstance{ProjectID: "proj-live", Status: string(domain.StatusActive)}
	instances.Items["proj-dying"] = &domain.DatabaseInstance{ProjectID: "proj-dying", Status: string(domain.StatusDeleting)}
	instances.Items["proj-grace"] = &domain.DatabaseInstance{ProjectID: "proj-grace", Status: string(domain.StatusPendingDeletion)}
	exists := projectExists(instances)
	for project, want := range map[string]bool{"proj-live": true, "proj-dying": false, "proj-grace": false, "proj-missing": false} {
		if got, err := exists(context.Background(), project); err != nil || got != want {
			t.Errorf("%s: %v %v, want %v", project, got, err, want)
		}
	}
	instances.Err = errors.New("down")
	if _, err := exists(context.Background(), "proj-live"); err == nil {
		t.Error("a store that cannot answer is an error, not a refusal")
	}
}

type memVault map[string]map[string]string

func (m memVault) Get(path string) (map[string]string, error) {
	record, ok := m[path]
	if !ok {
		return nil, vault.ErrNotFound
	}
	return record, nil
}
func (m memVault) Put(path string, data map[string]string) error {
	m[path] = data
	return nil
}

func matrixRegistryToken(t *testing.T) http.Handler {
	t.Helper()
	token, err := appregistry.NewTokenHandler(appregistrytest.New(t), func(context.Context, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// dbOnlyStore is a platform store whose handle opens no connection until used.
type dbOnlyStore struct {
	storage.PlatformStore
	db *sql.DB
}

func (s dbOnlyStore) DB() *sql.DB { return s.db }

func TestStartRegistrySweeper_OnlyWhereTheRegistryIssues(t *testing.T) {
	guard, err := appregistry.NewInternal(appregistrytest.Host, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stop, err := startRegistrySweeper(config.AppConfig{}, nil, nil, guard); err != nil || stop == nil {
		t.Fatalf("a guard only registry sweeps nothing: %v", err)
	}
	internal := appregistrytest.New(t)
	db, err := sql.Open("postgres", "postgres://unused.invalid/db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := dbOnlyStore{db: db}
	if _, err := startRegistrySweeper(config.AppConfig{AppRegistry: config.AppRegistryConfig{URL: "ftp://zot"}}, store, nil, internal); err == nil {
		t.Error("a registry address provisioning cannot call")
	}
	asked := make(chan struct{}, 1)
	zot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked <- struct{}{}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(zot.Close)
	stop, err := startRegistrySweeper(config.AppConfig{AppRegistry: config.AppRegistryConfig{URL: zot.URL}}, store, fakestore.NewInstances(), internal)
	if err != nil {
		t.Fatal(err)
	}
	<-asked
	stop()
}
