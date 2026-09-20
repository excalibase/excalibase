package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/go-chi/chi/v5"
)

// svc-auth and svc-graphql fetch a tenant's database credentials from the
// vault secret route. It never looked at the project row, so the engine
// could obtain working credentials for a project the platform must not serve
// and go on serving it.
func TestVaultSecretRouteRefusesProjectsThatAreNotServable(t *testing.T) {
	for name, status := range map[string]string{
		"restoring": string(domain.StatusRestoring),
		"deleting":  string(domain.StatusDeleting),
	} {
		t.Run(name, func(t *testing.T) {
			h, instances := vaultHandlerWithProject(t, "proj-1", status)
			_ = instances

			w := getVaultSecret(t, h, "projects/proj-1/credentials/excalibase_app")
			// 404, not 409: a service must treat the project as absent and
			// stop serving it, not retry.
			if w.Code != http.StatusNotFound {
				t.Errorf("status: got %d, want 404; body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestVaultSecretRouteServesALiveProject(t *testing.T) {
	h, _ := vaultHandlerWithProject(t, "proj-1", "ACTIVE")

	w := getVaultSecret(t, h, "projects/proj-1/credentials/excalibase_app")
	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

// Paths that are not a project's are untouched by the gate.
func TestVaultSecretRouteLeavesNonProjectPathsAlone(t *testing.T) {
	h, _ := vaultHandlerWithProject(t, "proj-1", string(domain.StatusRestoring))

	for _, path := range []string{"pki/signing-key", "backup/s3", "projects"} {
		w := getVaultSecret(t, h, path)
		if w.Code == http.StatusNotFound && path != "projects" {
			t.Errorf("%s must not be gated by the project lookup, got %d", path, w.Code)
		}
	}
}

func getVaultSecret(t *testing.T, h *VaultHandler, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/api/vault/secrets/*", h.GetSecret)
	req := httptest.NewRequest(http.MethodGet, "/api/vault/secrets/"+path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// vaultHandlerWithProject builds a vault handler whose project lookups see
// one project in the given state, with a credential filed for it.
func vaultHandlerWithProject(t *testing.T, projectID, status string) (*VaultHandler, *fakestore.Instances) {
	t.Helper()
	v, err := vault.NewWithStore(vault.NewMemoryStore())
	if err != nil {
		t.Fatalf("new vault: %v", err)
	}
	if _, err := v.Init(1, 1); err != nil {
		t.Fatalf("init vault: %v", err)
	}
	for _, path := range []string{
		"projects/" + projectID + "/credentials/excalibase_app",
		"pki/signing-key",
		"backup/s3",
	} {
		if err := v.Put(path, map[string]string{"password": "pw"}); err != nil {
			t.Fatalf("seed %s: %v", path, err)
		}
	}
	instances := fakestore.NewInstances()
	instances.Create(&domain.DatabaseInstance{ProjectID: projectID, OrgID: "org", Status: status})

	h := NewVaultHandler(v)
	h.SetInstanceStore(instances)
	return h, instances
}

func TestProjectIDForSecretOnlyMatchesProjectPaths(t *testing.T) {
	cases := map[string]string{
		"projects/p1/credentials/admin": "p1",
		"projects/p1/x":                 "p1",
		"projects/p1":                   "",
		"projects/":                     "",
		"projects":                      "",
		"pki/signing-key":               "",
		"backup/s3":                     "",
		"":                              "",
	}
	for path, want := range cases {
		if got := projectIDForSecret(path); got != want {
			t.Errorf("%q: got %q, want %q", path, got, want)
		}
	}
}

// A store that cannot answer must close the door, not open it.
func TestVaultSecretRouteRefusesWhenTheProjectCannotBeRead(t *testing.T) {
	h, _ := vaultHandlerWithProject(t, "proj-1", "ACTIVE")
	h.SetInstanceStore(&errorInstanceStore{Instances: *fakestore.NewInstances()})

	if w := getVaultSecret(t, h, "projects/proj-1/credentials/excalibase_app"); w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", w.Code)
	}
}

// errorInstanceStore fails every project lookup.
type errorInstanceStore struct{ fakestore.Instances }

func (*errorInstanceStore) FindByProjectID(string) (*domain.DatabaseInstance, error) {
	return nil, errUnreadableStore
}

var errUnreadableStore = errors.New("platform db unreachable")
