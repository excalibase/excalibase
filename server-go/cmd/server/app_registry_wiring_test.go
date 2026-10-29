package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/appregistry"
	"github.com/excalibase/provisioning-poc/internal/appregistry/appregistrytest"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/features"
)

func registryTokenRouter(t *testing.T, on ...features.Feature) (http.Handler, *appregistry.Internal) {
	t.Helper()
	internal := appregistrytest.New(t)
	cfg := config.AppConfig{DeploymentMode: "selfhosted", AppHostingEnabled: true}
	router, _, _ := buildMatrixWith(t, cfg, func(deps *handlerDeps) {
		deps.features = features.NewStatic(on...)
		deps.registryToken = appregistry.NewTokenHandler(internal.Issuer(), func(context.Context, string) (bool, error) { return true, nil })
	})
	return router, internal
}

func askRegistryToken(handler http.Handler, internal *appregistry.Internal) int {
	req := httptest.NewRequest(http.MethodGet, registryTokenPath+"?service="+appregistrytest.Host+"&scope=repository:proj-a/api:pull", nil)
	cred := internal.PullCredential("proj-a")
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
	internal, err := newInternalRegistry(on)
	if err != nil || internal == nil || internal.Host() != appregistrytest.Host {
		t.Fatalf("on: %v %v", internal, err)
	}
	off := on
	off.DarkFeatures = nil
	if internal, err := newInternalRegistry(off); internal != nil || err != nil {
		t.Errorf("flag off: %v %v", internal, err)
	}
	bad := on
	bad.AppRegistry.TokenKeyPEM = []byte("not a key")
	if _, err := newInternalRegistry(bad); err == nil {
		t.Error("a key that cannot sign must stop the boot")
	}
}
