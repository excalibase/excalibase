//go:build integration

package handler

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	pgtest "github.com/excalibase/provisioning-poc/internal/testutil/pgstore"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

// EXC-662: the in-process vault shares the platform pool. Concurrent enables,
// for one project and for several, must neither hang on a small pool nor
// leave a project with more than the one key its factors are sealed with.
func TestAuthMFA_ConcurrentEnablesOnThePostgresVaultKeepOneKeyAndNeverHang(t *testing.T) {
	store := pgtest.New(t)
	store.DB().SetMaxOpenConns(2)
	localVault, err := vault.NewWithStore(vault.NewPostgresStore(store.DB()))
	if err != nil {
		t.Fatal(err)
	}
	result, err := localVault.Init(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localVault.Unseal(result.Shares[0]); err != nil {
		t.Fatal(err)
	}
	projects := []string{"proj_mfa_c1", "proj_mfa_c2", "proj_mfa_c3", "proj_mfa_c4"}
	for _, id := range projects {
		if err := store.Create(&domain.DatabaseInstance{ProjectID: id, OrgID: "org-mfa", Status: "ACTIVE"}); err != nil {
			t.Fatal(err)
		}
	}
	h := NewProvisioningHandler(service.NewProvisioningService(store, provisioner.NewFactory(), nil), store)
	h.SetAuthSettingsStore(store)
	h.SetAuthOAuth(AuthOAuthDeps{Flags: features.NewStatic(features.AuthMFA), SignIn: store, MFA: store, Secrets: localVault, Audit: &recordingAudit{}})
	router := chi.NewRouter()
	router.Put("/api/projects/{projectId}/auth-settings/mfa", h.PutAuthMFA)

	enableAll(t, router, projects)
	first := map[string]string{}
	for _, id := range projects {
		stored, err := localVault.Get(domain.MFAKeyPath(id))
		if err != nil || !domain.ValidMFAKey(stored[domain.MFAKeyField]) {
			t.Fatalf("%s: no valid key after enabling: %v", id, err)
		}
		first[id] = stored[domain.MFAKeyField]
	}
	// A second wave finds the keys and must keep every one of them.
	enableAll(t, router, projects)
	for _, id := range projects {
		if stored, _ := localVault.Get(domain.MFAKeyPath(id)); stored[domain.MFAKeyField] != first[id] {
			t.Errorf("%s: the key changed", id)
		}
		if signIn, err := store.GetAuthSignIn(t.Context(), id); err != nil || !signIn.MFAEnabled {
			t.Errorf("%s: switch = %+v, %v", id, signIn, err)
		}
	}
	var keys int
	if err := store.DB().QueryRow(`SELECT count(*) FROM vault_secrets WHERE path LIKE 'projects/proj_mfa_c%/auth/mfa'`).Scan(&keys); err != nil || keys != len(projects) {
		t.Fatalf("keys = %d, %v", keys, err)
	}
}

// enableAll sends five concurrent enables per project and fails on a hang.
func enableAll(t *testing.T, router chi.Router, projects []string) {
	t.Helper()
	codes := make(chan int, len(projects)*5)
	var wg sync.WaitGroup
	for _, id := range projects {
		for range 5 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes <- doJSON(router, http.MethodPut, "/api/projects/"+id+"/auth-settings/mfa", map[string]any{"enabled": true}).Code
			}()
		}
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("concurrent enables hung on a two-connection pool")
	}
	close(codes)
	for code := range codes {
		if code != http.StatusOK {
			t.Errorf("an enable answered %d", code)
		}
	}
}
