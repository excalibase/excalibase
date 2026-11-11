package sites_test

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

func newSecretsHarness(t *testing.T, secrets *sitestest.Secrets) *serviceHarness {
	t.Helper()
	store, publisher, origins := sitestest.NewMemoryStore(), sitestest.NewPublisher(), sitestest.NewOrigins()
	options := serverOptions(t, features.NewStatic(features.SitesServer), store)
	options.Secrets = secrets
	service, err := sites.NewService(store, publisher, origins, options)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return &serviceHarness{service: service, store: store, publisher: publisher, origins: origins}
}

func secretBinding(version cfworkers.ScriptVersion, name string) (string, bool) {
	for _, binding := range version.Bindings {
		if binding.Name == name && binding.Type == cfworkers.BindingSecretText {
			return binding.Text, true
		}
	}
	return "", false
}

func TestOnlyServerVisibleSecretsAreBoundAsSecretText(t *testing.T) {
	secrets := sitestest.NewSecrets(map[string]string{"STRIPE_KEY": "sk_1", "DB_PASSWORD": "hunter2"})
	h := newSecretsHarness(t, secrets)
	ctx := context.Background()
	if err := h.service.SetServerVisibleSecrets(ctx, testProject, []string{"STRIPE_KEY"}, testActor); err != nil {
		t.Fatalf("SetServerVisibleSecrets: %v", err)
	}
	site := h.createServerSite(t, "ssr")
	if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 1")); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	version := h.publisher.Versions[sites.ScriptName(site)]
	if got, ok := secretBinding(version, "STRIPE_KEY"); !ok || got != "sk_1" {
		t.Errorf("STRIPE_KEY = %q, bound = %v", got, ok)
	}
	if _, ok := bindingText(version, "DB_PASSWORD"); ok {
		t.Error("a secret nobody marked reached the Worker")
	}
	if _, ok := secretBinding(version, "DB_PASSWORD"); ok {
		t.Error("a secret nobody marked reached the Worker as secret text")
	}
}

func TestARollbackBindsTheSecretsCurrentValue(t *testing.T) {
	secrets := sitestest.NewSecrets(map[string]string{"API_TOKEN": "old"})
	h := newSecretsHarness(t, secrets)
	ctx := context.Background()
	if err := h.service.SetServerVisibleSecrets(ctx, testProject, []string{"API_TOKEN"}, testActor); err != nil {
		t.Fatal(err)
	}
	site := h.createServerSite(t, "ssr")
	first, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 'v1'"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 'v2'")); err != nil {
		t.Fatal(err)
	}
	secrets.Values["API_TOKEN"] = "rotated"
	if _, err := h.service.Rollback(ctx, testProject, site.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := secretBinding(h.publisher.Versions[sites.ScriptName(site)], "API_TOKEN"); got != "rotated" {
		t.Fatalf("API_TOKEN after rollback = %q, want the current value (secrets are bound by name)", got)
	}
}

func TestASecretWinsOverAPlainVariableOfTheSameName(t *testing.T) {
	h := newSecretsHarness(t, sitestest.NewSecrets(map[string]string{"TOKEN": "secret"}))
	ctx := context.Background()
	if err := h.service.SetServerVisibleSecrets(ctx, testProject, []string{"TOKEN"}, testActor); err != nil {
		t.Fatal(err)
	}
	site := h.createServerSite(t, "ssr")
	if err := h.service.SetRuntimeVars(ctx, testProject, site.ID, map[string]string{"TOKEN": "plain"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 1")); err != nil {
		t.Fatal(err)
	}
	version := h.publisher.Versions[sites.ScriptName(site)]
	count := 0
	for _, binding := range version.Bindings {
		if binding.Name == "TOKEN" {
			count++
		}
	}
	if got, ok := secretBinding(version, "TOKEN"); !ok || got != "secret" || count != 1 {
		t.Fatalf("TOKEN bound %d times, secret = %q %v", count, got, ok)
	}
}

func TestAServerPublishFailsWhenTheSecretsCannotBeRead(t *testing.T) {
	secrets := sitestest.NewSecrets(map[string]string{"TOKEN": "x"})
	h := newSecretsHarness(t, secrets)
	if err := h.service.SetServerVisibleSecrets(context.Background(), testProject, []string{"TOKEN"}, testActor); err != nil {
		t.Fatal(err)
	}
	secrets.Err = errors.New("vault sealed")
	site := h.createServerSite(t, "ssr")
	if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 1")); err == nil {
		t.Fatal("published without the secrets the site was told it has")
	}
	if len(h.publisher.Versions) != 0 {
		t.Fatal("a version went out without its secrets")
	}
}

func TestNothingReadsTheVaultWhenNoSecretIsMarked(t *testing.T) {
	secrets := sitestest.NewSecrets(map[string]string{"TOKEN": "x"})
	secrets.Err = errors.New("vault sealed")
	h := newSecretsHarness(t, secrets)
	site := h.createServerSite(t, "ssr")
	if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 1")); err != nil {
		t.Fatalf("a site with no marked secrets needs no vault: %v", err)
	}
}

func TestMarkingRefusesUnknownAndReservedSecretsAndWhileOff(t *testing.T) {
	h := newSecretsHarness(t, sitestest.NewSecrets(map[string]string{"EXCALIBASE_URL": "x", "OK": "y"}))
	ctx := context.Background()
	var input *sites.InputError
	for _, names := range [][]string{{"MISSING"}, {"EXCALIBASE_URL"}} {
		if err := h.service.SetServerVisibleSecrets(ctx, testProject, names, testActor); !errors.As(err, &input) {
			t.Errorf("%v: err = %v, want an input error", names, err)
		}
	}
	if err := h.service.SetServerVisibleSecrets(ctx, testProject, []string{"OK"}, testActor); err != nil {
		t.Fatal(err)
	}
	listed, err := h.service.ServerSecretVisibility(ctx, testProject)
	if err != nil || len(listed) != 2 {
		t.Fatalf("listed = %+v err=%v", listed, err)
	}
	for _, row := range listed {
		if row.ServerVisible != (row.Name == "OK") {
			t.Errorf("row = %+v", row)
		}
	}
	off := newServerHarness(t, false)
	if err := off.service.SetServerVisibleSecrets(ctx, testProject, nil, testActor); !errors.Is(err, sites.ErrServerSitesOff) {
		t.Errorf("flag off: err = %v", err)
	}
}

// Marking checks the reserved names, but the list can grow and an old marking can predate it: a publish never binds one.
func TestAPublishNeverLetsASecretDisplaceAPlatformBinding(t *testing.T) {
	secrets := sitestest.NewSecrets(map[string]string{"EXCALIBASE_URL": "https://evil.test", "ASSETS": "x", "bad-name": "x", "OK": "fine"})
	h := newSecretsHarness(t, secrets)
	ctx := context.Background()
	// Written straight to the store, as an older release (with a shorter reserved list) would have.
	if err := h.store.SetServerVisibleSecrets(ctx, testProject, []string{"EXCALIBASE_URL", "ASSETS", "bad-name", "OK"}, testActor); err != nil {
		t.Fatal(err)
	}
	site := h.createServerSite(t, "ssr")
	if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 1")); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	version := h.publisher.Versions[sites.ScriptName(site)]
	if got, plain := bindingText(version, "EXCALIBASE_URL"); !plain || got != "https://api.example.test" {
		t.Errorf("EXCALIBASE_URL = %q plain=%v, the platform's value must stand", got, plain)
	}
	for _, name := range []string{"EXCALIBASE_URL", "ASSETS", "bad-name"} {
		if _, ok := secretBinding(version, name); ok {
			t.Errorf("%s was bound as a secret", name)
		}
	}
	if got, ok := secretBinding(version, "OK"); !ok || got != "fine" {
		t.Errorf("a harmless marked secret was dropped: %q %v", got, ok)
	}
}
