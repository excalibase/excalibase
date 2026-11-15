package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
)

const storedOwnerSecret = "stored-owner-secret-0123456789"

// storedSecretsVault holds one owner login and reads nothing else.
type storedSecretsVault struct{ vaultclient.VaultClient }

func (storedSecretsVault) Sealed() bool { return false }
func (storedSecretsVault) List(string) ([]string, error) {
	return []string{"projects/proj-a/credentials/admin"}, nil
}
func (storedSecretsVault) Get(path string) (map[string]string, error) {
	return map[string]string{"username": "owner", "password": storedOwnerSecret}, nil
}

// passwordRouter is the production router with the credentials flag on and a
// vault that holds the owner's password for proj-a.
func passwordRouter(t *testing.T) (http.Handler, callers, callers) {
	t.Helper()
	cfg := config.AppConfig{DeploymentMode: "selfhosted", DarkFeatures: []features.Feature{features.OneTimeCredentials}}
	return buildMatrixWith(t, cfg, func(deps *handlerDeps) {
		port := 5432
		store := fakestore.NewInstances()
		store.Create(&domain.DatabaseInstance{ProjectID: matrixProjectA, OrgID: matrixOrgA, Status: "ACTIVE", Username: "owner",
			Host: "db.internal", Port: &port, DatabaseName: "app", SSLMode: "require"})
		svc := service.NewProvisioningService(store, provisioner.NewFactory(), k8s.NewMockClient())
		svc.SetVault(storedSecretsVault{})
		svc.SetFeatures(cfg.Features())
		deps.provHandler = handler.NewProvisioningHandler(svc, nil)
	})
}

func bodyOf(router http.Handler, method, path, token string) (int, string) {
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func TestAMemberWithoutAdminCannotGetTheStoredPasswordByAnyRoute(t *testing.T) {
	router, who, _ := passwordRouter(t)
	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/provision/proj-a/credentials"},
		{http.MethodPost, "/api/provision/proj-a/credentials/rotate"},
		{http.MethodGet, "/api/vault/secrets/projects/proj-a/credentials/admin"},
		{http.MethodGet, "/api/vault/secrets-list?prefix=projects/proj-a/"},
		{http.MethodGet, "/api/provision/proj-a/"},
		{http.MethodGet, "/api/projects/proj-a/info/"},
	}
	for _, route := range routes {
		for _, caller := range []string{callerDeveloper, callerReadOnly, callerOtherOrg, callerBoundElsewhere, callerAnonymous} {
			code, body := bodyOf(router, route.method, route.path, who[caller])
			if strings.Contains(body, storedOwnerSecret) {
				t.Errorf("%s %s as %s leaked the stored password: %s", route.method, route.path, caller, body)
			}
			if route.path != "/api/provision/proj-a/" && route.path != "/api/projects/proj-a/info/" && code < 400 {
				t.Errorf("%s %s as %s answered %d", route.method, route.path, caller, code)
			}
		}
	}
}

func TestAnAdminReadingCredentialsGetsNoPassword(t *testing.T) {
	router, _, admins := passwordRouter(t)
	code, body := bodyOf(router, http.MethodGet, "/api/provision/proj-a/credentials", admins[callerAdminSession])
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	if strings.Contains(strings.ToLower(body), "password\":") && !strings.Contains(body, "<password>") || strings.Contains(body, storedOwnerSecret) {
		t.Errorf("the credentials answer carries a password: %s", body)
	}
	if strings.Contains(body, `"password"`) || strings.Contains(body, `"readerPassword"`) {
		t.Errorf("the credentials answer has a password field: %s", body)
	}
}
