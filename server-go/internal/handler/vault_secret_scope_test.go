package handler

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/go-chi/chi/v5"
)

// EXC-418: a human caller holding view_credentials could read ANY project's
// database credentials, and nothing recorded that they had. The read is now
// bound to the project the path names, and every read leaves a line naming who
// read what — never the value.

const (
	vaultOrgA       = "org-a"
	vaultProjectA   = "proj-vault-a"
	vaultProjectB   = "proj-vault-b"
	vaultMemberA    = "member-vault-a"
	vaultSecretPath = "projects/" + vaultProjectA + "/credentials/excalibase_app"
)

func vaultScopeHandler(t *testing.T) *VaultHandler {
	t.Helper()
	v, err := vault.NewWithStore(vault.NewMemoryStore())
	if err != nil {
		t.Fatalf("new vault: %v", err)
	}
	res, err := v.Init(1, 1)
	if err != nil {
		t.Fatalf("init vault: %v", err)
	}
	if _, err := v.Unseal(res.Shares[0]); err != nil {
		t.Fatalf("unseal vault: %v", err)
	}
	if err := v.Put(vaultSecretPath, map[string]string{"password": "s3cr3t"}); err != nil {
		t.Fatalf("seed secret: %v", err)
	}

	instances := fakestore.NewInstances()
	instances.Items[vaultProjectA] = &domain.DatabaseInstance{ProjectID: vaultProjectA, OrgID: vaultOrgA, Status: "ACTIVE"}
	instances.Items[vaultProjectB] = &domain.DatabaseInstance{ProjectID: vaultProjectB, OrgID: "org-b", Status: "ACTIVE"}
	orgs := fakestore.NewOrgs()
	orgs.AddMember(vaultOrgA, vaultMemberA, domain.OrgRoleAdmin)

	h := NewVaultHandler(v)
	h.SetInstanceStore(instances)
	h.SetOrgStore(orgs)
	return h
}

// vaultSecretRead drives GET /secrets/<path> as the given caller.
func vaultSecretRead(t *testing.T, h *VaultHandler, path string, user *domain.User, token *domain.AccessToken) (int, string) {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/secrets/*", h.GetSecret)
	req := httptest.NewRequest(http.MethodGet, "/secrets/"+path, nil)
	ctx := req.Context()
	if user != nil {
		ctx = auth.SetUser(ctx, user)
	}
	if token != nil {
		ctx = auth.SetToken(ctx, token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req.WithContext(ctx))
	return w.Code, w.Body.String()
}

func TestVaultSecretReadIsBoundToTheProjectThePathNames(t *testing.T) {
	h := vaultScopeHandler(t)
	outsider := &domain.User{ID: "outsider", Role: "user", Active: true}

	code, body := vaultSecretRead(t, h, vaultSecretPath, outsider, &domain.AccessToken{})
	if code != http.StatusNotFound {
		t.Fatalf("got %d (%s), want 404", code, strings.TrimSpace(body))
	}
	if strings.Contains(body, "s3cr3t") {
		t.Fatal("the refusal carried the secret")
	}
}

func TestVaultSecretReadAllowsAMemberOfTheProjectsOrg(t *testing.T) {
	h := vaultScopeHandler(t)
	member := &domain.User{ID: vaultMemberA, Role: "user", Active: true}

	code, body := vaultSecretRead(t, h, vaultSecretPath, member, &domain.AccessToken{})
	if code != http.StatusOK {
		t.Fatalf("got %d (%s), want 200", code, strings.TrimSpace(body))
	}
}

func TestVaultSecretReadAllowsAPlatformOperator(t *testing.T) {
	h := vaultScopeHandler(t)
	operator := &domain.User{ID: "op", Role: "platform_operator", Active: true}

	code, body := vaultSecretRead(t, h, vaultSecretPath, operator, &domain.AccessToken{})
	if code != http.StatusOK {
		t.Fatalf("got %d (%s), want 200", code, strings.TrimSpace(body))
	}
}

func TestVaultSecretReadRefusesATokenBoundElsewhere(t *testing.T) {
	h := vaultScopeHandler(t)
	operator := &domain.User{ID: "op", Role: "platform_operator", Active: true}

	code, _ := vaultSecretRead(t, h, vaultSecretPath, operator, &domain.AccessToken{ProjectID: vaultProjectB})
	if code != http.StatusNotFound {
		t.Fatalf("got %d, want 404: a token bound to project B read project A's secret", code)
	}
}

// A capability token is the platform's own service principal. The capability
// gate above the route already names the one secret it may read, so this
// handler must leave it exactly as it was.
func TestVaultSecretReadLeavesServicePrincipalsAlone(t *testing.T) {
	h := vaultScopeHandler(t)
	svc := &domain.User{ID: "svc-auth", Role: "platform_admin", Active: true, Kind: domain.UserKindService}
	token := &domain.AccessToken{Permissions: []string{"vault:read:" + vaultSecretPath}}

	code, body := vaultSecretRead(t, h, vaultSecretPath, svc, token)
	if code != http.StatusOK {
		t.Fatalf("got %d (%s), want 200", code, strings.TrimSpace(body))
	}
}

func TestVaultSecretReadRefusesAnUnauthenticatedCaller(t *testing.T) {
	h := vaultScopeHandler(t)
	code, _ := vaultSecretRead(t, h, vaultSecretPath, nil, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", code)
	}
}

func TestVaultSecretReadIsAudited(t *testing.T) {
	h := vaultScopeHandler(t)
	var logged bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(previous) })

	member := &domain.User{ID: vaultMemberA, Role: "user", Active: true}
	if code, body := vaultSecretRead(t, h, vaultSecretPath, member, &domain.AccessToken{}); code != http.StatusOK {
		t.Fatalf("got %d (%s)", code, strings.TrimSpace(body))
	}

	line := logged.String()
	for _, want := range []string{"vault.secret.read", vaultMemberA, vaultProjectA, vaultSecretPath} {
		if !strings.Contains(line, want) {
			t.Errorf("audit line %q does not name %q", strings.TrimSpace(line), want)
		}
	}
	if strings.Contains(line, "s3cr3t") {
		t.Fatal("the audit line carried the secret value")
	}
}
