package handler

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
)

const credentialValue = "sk_live_pasted_by_the_owner"

func credentialVarsRouter(t *testing.T, v vaultclient.VaultClient) (chi.Router, *fakeAppStore) {
	t.Helper()
	store := newFakeAppStore()
	h := NewAppHandler(store, newFakeSources("storefront_db"), testAppRoute)
	h.SetAppLimits(fakeAppLimits{limit: testAppLimit})
	if v != nil {
		h.SetSecretVault(v)
	}
	r := chi.NewRouter()
	r.Use(callerWithProjectRole(domain.OrgRoleDeveloper))
	r.Route("/api/projects/{projectId}/apps", func(r chi.Router) { h.Routes(r) })
	return r, store
}

func createBody(env ...map[string]any) map[string]any {
	body := validAppBody()
	body["env"] = env
	return body
}

func literal(name, value string) map[string]any {
	return map[string]any{"name": name, "kind": "literal", "value": value}
}

// EXC-761: a credential-looking name is a secret on the server too, so a
// client that skips Studio's form cannot park a key in a plain variable.
func TestCreateSealsCredentialLookingPlainVariables(t *testing.T) {
	vault := newFakeVault()
	r, store := credentialVarsRouter(t, vault)

	w := doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/", createBody(
		literal("STRIPE_API_KEY", credentialValue), literal("MODE", "production")))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), credentialValue) {
		t.Fatalf("the answer carries the value: %s", w.Body.String())
	}
	for _, app := range store.apps {
		for _, variable := range app.Env {
			switch variable.Name {
			case "STRIPE_API_KEY":
				if variable.Kind != apphost.KindSecret || variable.Value != nil || variable.Secret == nil {
					t.Fatalf("STRIPE_API_KEY must be a secret, got %+v", variable)
				}
				if got := vault.data[variable.Secret.Path][variable.Secret.Key]; got != credentialValue {
					t.Fatalf("the value was not stored under the secret: %q", got)
				}
			case "MODE":
				if variable.Kind != apphost.KindLiteral {
					t.Fatalf("an ordinary name stays plain, got %+v", variable)
				}
			}
		}
	}
}

// The two prod apps hold key-like plain variables: saving them must keep
// working, converting the variable and keeping its value.
func TestUpdateSealsALegacyPlainCredentialAndKeepsItsValue(t *testing.T) {
	vault := newFakeVault()
	r, store := credentialVarsRouter(t, vault)
	value := credentialValue
	store.apps[appKey(appTestProject, "app-legacy")] = apphost.App{
		ID: "app-legacy", ProjectID: appTestProject, Name: "legacy", Image: "nginx:1", Port: 8080, Replicas: 1, Version: 1,
		Tier: domain.Standard, Status: apphost.StatusCreated,
		Env: []apphost.EnvVar{{Name: "API_TOKEN", Kind: apphost.KindLiteral, Value: &value}},
	}

	// An unrelated edit, not touching the variable.
	w := doAppRequest(t, r, http.MethodPatch, "/api/projects/"+appTestProject+"/apps/app-legacy/", map[string]any{"replicas": 2})
	if w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	saved := store.apps[appKey(appTestProject, "app-legacy")]
	got := saved.Env[0]
	if got.Kind != apphost.KindSecret || got.Value != nil || got.Secret == nil {
		t.Fatalf("the legacy variable must become a secret, got %+v", got)
	}
	if vault.data[got.Secret.Path][got.Secret.Key] != credentialValue {
		t.Fatalf("the value must be kept: %v", vault.data)
	}

	// A client that sends the plain variable back is converted the same way.
	w = doAppRequestWithVersion(t, r, http.MethodPatch, "/api/projects/"+appTestProject+"/apps/app-legacy/", map[string]any{
		"env": []map[string]any{literal("DB_PASSWORD", "hunter2")},
	}, 2)
	if w.Code != http.StatusOK {
		t.Fatalf("update with a plain credential: %d %s", w.Code, w.Body.String())
	}
	saved = store.apps[appKey(appTestProject, "app-legacy")]
	if saved.Env[0].Kind != apphost.KindSecret || saved.Env[0].Value != nil {
		t.Fatalf("DB_PASSWORD must be a secret, got %+v", saved.Env[0])
	}
}

// With no vault the value cannot be kept safely, so the write is refused and
// nothing is saved as plain.
func TestCredentialLookingVariableIsRefusedWhenNoVaultCanHoldIt(t *testing.T) {
	r, store := credentialVarsRouter(t, nil)
	w := doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/", createBody(literal("API_KEY", credentialValue)))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d %s, want 503", w.Code, w.Body.String())
	}
	if len(store.apps) != 0 {
		t.Fatal("an app was saved with the key as a plain value")
	}
}

// A write that is refused after its values were stored removes only what it
// created. A secret the app already held under that name keeps its old value.
func TestRefusedUpdateRestoresAnExistingSecretAndRemovesAFreshOne(t *testing.T) {
	vault := newFakeVault()
	store := &conflictingAppStore{newFakeAppStore()}
	h := NewAppHandler(store, newFakeSources("storefront_db"), testAppRoute)
	h.SetAppLimits(fakeAppLimits{limit: testAppLimit})
	h.SetSecretVault(vault)
	r := chi.NewRouter()
	r.Use(callerWithProjectRole(domain.OrgRoleDeveloper))
	r.Route("/api/projects/{projectId}/apps", func(r chi.Router) { h.Routes(r) })

	secretRef := apphost.AppSecretRef(appTestProject, "app-1", "API_TOKEN")
	vault.data[secretRef.Path] = map[string]string{secretRef.Key: "the-old-value"}
	existing := secretRef
	store.apps[appKey(appTestProject, "app-1")] = apphost.App{
		ID: "app-1", ProjectID: appTestProject, Name: "web", Image: "nginx:1", Port: 8080, Replicas: 1, Version: 1,
		Tier: domain.Standard, Status: apphost.StatusCreated,
		Env: []apphost.EnvVar{{Name: "API_TOKEN", Kind: apphost.KindSecret, Secret: &existing}},
	}

	w := doAppRequest(t, r, http.MethodPatch, "/api/projects/"+appTestProject+"/apps/app-1/", map[string]any{
		"env": []map[string]any{literal("API_TOKEN", "the-new-value"), literal("NEW_SECRET", "fresh")},
	})
	if w.Code == http.StatusOK {
		t.Fatalf("the conflicting store must refuse the write, got %d", w.Code)
	}
	if got := vault.data[secretRef.Path][secretRef.Key]; got != "the-old-value" {
		t.Fatalf("the existing secret was lost or changed: %q", got)
	}
	fresh := apphost.AppSecretRef(appTestProject, "app-1", "NEW_SECRET")
	if _, kept := vault.data[fresh.Path]; kept {
		t.Fatal("a value stored for the refused write was left behind")
	}
}

type failingGetVault struct{ *fakeVault }

func (failingGetVault) Get(string) (map[string]string, error) { return nil, errors.New("vault down") }

// A vault that cannot be read refuses the write: absence and failure differ.
func TestCredentialVarRefusedWhenTheVaultCannotBeRead(t *testing.T) {
	r, store := credentialVarsRouter(t, failingGetVault{newFakeVault()})
	w := doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/",
		createBody(literal("STRIPE_API_KEY", credentialValue)))
	if w.Code != http.StatusServiceUnavailable || len(store.apps) != 0 {
		t.Fatalf("got %d with %d apps saved, want 503 and none", w.Code, len(store.apps))
	}
}

type putFailsOnSecondVault struct {
	*fakeVault
	puts    int
	deleted []string
}

func (v *putFailsOnSecondVault) Put(p string, d map[string]string) error {
	v.puts++
	if v.puts == 2 {
		return errors.New("vault refused")
	}
	return v.fakeVault.Put(p, d)
}

func (v *putFailsOnSecondVault) Delete(p string) error {
	v.deleted = append(v.deleted, p)
	return errors.New("delete refused")
}

// A store that fails halfway undoes what it wrote, and an undo that itself
// fails is logged, never fatal.
func TestCredentialVarPartialStoreFailureIsUndone(t *testing.T) {
	vault := &putFailsOnSecondVault{fakeVault: newFakeVault()}
	r, store := credentialVarsRouter(t, vault)
	w := doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/",
		createBody(literal("STRIPE_API_KEY", credentialValue), literal("DB_PASSWORD", "hunter2")))
	if w.Code != http.StatusInternalServerError || len(store.apps) != 0 {
		t.Fatalf("got %d with %d apps saved, want 500 and none", w.Code, len(store.apps))
	}
	if len(vault.deleted) != 2 {
		t.Fatalf("undo must try both paths, tried %v", vault.deleted)
	}
}
