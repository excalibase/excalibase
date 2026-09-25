package service

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

const (
	resolverProject  = "proj-res1"
	resolverDatabase = "appdb"
	resolverHost     = "proj-res1-postgres-rw.org1-proj-res1.svc.cluster.local"
	ownerPassword    = "p@ss/w:rd?#1"
)

func resolverFixture() (*AppEnvResolver, *fakeVault, *fakestore.Instances) {
	vault := newFakeVault()
	vault.data[vaultCredentialPath(resolverProject, roleAdmin)] = map[string]string{
		"host": resolverHost, "port": "5432", "database": resolverDatabase,
		"username": "owner_user", "password": ownerPassword,
	}
	vault.data[vaultCredentialPath(resolverProject, roleApp)] = map[string]string{
		"host": resolverHost, "port": "5432", "database": resolverDatabase,
		"username": "excalibase_app", "password": "platform-only",
	}
	instances := fakestore.NewInstances()
	instances.Items[resolverProject] = &domain.DatabaseInstance{
		ProjectID: resolverProject, Status: string(domain.StatusActive), DatabaseName: resolverDatabase,
		SSLMode: "require", Namespace: "org1-proj-res1",
	}
	return NewAppEnvResolver(vault, instances), vault, instances
}

func databaseRef(variable string) apphost.ResolvedReference {
	return apphost.ResolvedReference{
		Name:   variable,
		Target: apphost.ReferenceTarget{SourceKind: apphost.SourceDatabase, SourceName: resolverDatabase, Variable: variable},
		Scope:  apphost.ScopeInternal,
	}
}

func TestAppEnvResolver_DatabaseURLIsTheOwnerLoginInCluster(t *testing.T) {
	resolver, _, _ := resolverFixture()
	got, err := resolver.ResolveReference(resolverProject, databaseRef("DATABASE_URL"))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("DATABASE_URL does not parse: %v", err)
	}
	password, _ := parsed.User.Password()
	checks := map[string][2]string{
		"scheme":   {parsed.Scheme, "postgresql"},
		"user":     {parsed.User.Username(), "owner_user"},
		"password": {password, ownerPassword},
		"host":     {parsed.Host, resolverHost + ":5432"},
		"database": {parsed.Path, "/" + resolverDatabase},
		"sslmode":  {parsed.Query().Get("sslmode"), "require"},
	}
	for what, pair := range checks {
		if pair[0] != pair[1] {
			t.Errorf("%s = %q, want %q", what, pair[0], pair[1])
		}
	}
	if strings.Contains(got, "excalibase_app") {
		t.Errorf("an app must get the owner's login, not the platform's: %s", got)
	}
}

func TestAppEnvResolver_SingleDatabaseVariables(t *testing.T) {
	resolver, _, _ := resolverFixture()
	want := map[string]string{
		"PGHOST": resolverHost, "PGPORT": "5432", "PGDATABASE": resolverDatabase,
		"PGUSER": "owner_user", "PGPASSWORD": ownerPassword,
	}
	for variable, value := range want {
		got, err := resolver.ResolveReference(resolverProject, databaseRef(variable))
		if err != nil || got != value {
			t.Errorf("%s = %q (%v), want %q", variable, got, err, value)
		}
	}
}

func TestAppEnvResolver_ReferenceRefusals(t *testing.T) {
	cases := map[string]struct {
		mutate func(*fakeVault, *fakestore.Instances, *apphost.ResolvedReference)
		want   string
	}{
		"public scope": {func(_ *fakeVault, _ *fakestore.Instances, r *apphost.ResolvedReference) {
			r.Scope = apphost.ScopePublic
		}, "in-cluster"},
		"unknown source kind": {func(_ *fakeVault, _ *fakestore.Instances, r *apphost.ResolvedReference) {
			r.Target.SourceKind = "cache"
		}, "cache"},
		"unknown variable": {func(_ *fakeVault, _ *fakestore.Instances, r *apphost.ResolvedReference) {
			r.Target.Variable = "PGSSLKEY"
		}, "PGSSLKEY"},
		"no project row": {func(_ *fakeVault, i *fakestore.Instances, _ *apphost.ResolvedReference) {
			delete(i.Items, resolverProject)
		}, "no database"},
		"another database name": {func(_ *fakeVault, _ *fakestore.Instances, r *apphost.ResolvedReference) {
			r.Target.SourceName = "otherdb"
		}, "otherdb"},
		"database being restored": {func(_ *fakeVault, i *fakestore.Instances, _ *apphost.ResolvedReference) {
			i.Items[resolverProject].Status = string(domain.StatusRestoring)
		}, "not available"},
		"database still being built": {func(_ *fakeVault, i *fakestore.Instances, _ *apphost.ResolvedReference) {
			i.Items[resolverProject].Status = domain.StatusProvisioning
		}, "not available"},
		"no TLS setting recorded": {func(_ *fakeVault, i *fakestore.Instances, _ *apphost.ResolvedReference) {
			i.Items[resolverProject].SSLMode = ""
		}, "TLS"},
		"login missing from the vault": {func(v *fakeVault, _ *fakestore.Instances, _ *apphost.ResolvedReference) {
			delete(v.data, vaultCredentialPath(resolverProject, roleAdmin))
		}, "database login"},
		"login without a password": {func(v *fakeVault, _ *fakestore.Instances, _ *apphost.ResolvedReference) {
			v.data[vaultCredentialPath(resolverProject, roleAdmin)]["password"] = ""
		}, "password"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			resolver, vault, instances := resolverFixture()
			ref := databaseRef("DATABASE_URL")
			tc.mutate(vault, instances, &ref)
			got, err := resolver.ResolveReference(resolverProject, ref)
			if err == nil {
				t.Fatalf("must be refused, got %q", got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q must mention %q", err, tc.want)
			}
			if strings.Contains(err.Error(), ownerPassword) {
				t.Errorf("the refusal leaked the password: %v", err)
			}
		})
	}
}

type failingInstances struct{ *fakestore.Instances }

func (failingInstances) FindByProjectID(string) (*domain.DatabaseInstance, error) {
	return nil, errors.New("pq: connection refused 10.0.0.1:5432")
}

func TestAppEnvResolver_InstanceLookupFailureIsReportedPlainly(t *testing.T) {
	_, vault, instances := resolverFixture()
	resolver := NewAppEnvResolver(vault, failingInstances{instances})
	_, err := resolver.ResolveReference(resolverProject, databaseRef("DATABASE_URL"))
	if err == nil || strings.Contains(err.Error(), "10.0.0.1") {
		t.Fatalf("want a plain refusal, got %v", err)
	}
}

func TestAppEnvResolver_SecretReadsTheStoredValue(t *testing.T) {
	resolver, vault, _ := resolverFixture()
	ref := apphost.AppSecretRef(resolverProject, "app-1", "API_KEY")
	vault.data[ref.Path] = map[string]string{ref.Key: "sk_live_x"}
	got, err := resolver.ResolveSecret(resolverProject, ref)
	if err != nil || got != "sk_live_x" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestAppEnvResolver_SecretRefusals(t *testing.T) {
	resolver, vault, _ := resolverFixture()
	stored := apphost.AppSecretRef(resolverProject, "app-1", "API_KEY")
	vault.data[stored.Path] = map[string]string{"other": "x"}
	cases := map[string]apphost.SecretRef{
		"nothing stored":        apphost.AppSecretRef(resolverProject, "app-1", "MISSING"),
		"entry without the key": stored,
		"another project":       apphost.AppSecretRef("proj-other", "app-1", "API_KEY"),
	}
	for name, ref := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := resolver.ResolveSecret(resolverProject, ref); err == nil {
				t.Fatalf("must be refused, got %q", got)
			}
		})
	}
}

func TestAppEnvResolver_NoVaultRefusesInsteadOfPanicking(t *testing.T) {
	_, _, instances := resolverFixture()
	resolver := NewAppEnvResolver(nil, instances)
	if _, err := resolver.ResolveReference(resolverProject, databaseRef("DATABASE_URL")); err == nil {
		t.Error("a reference with no vault must be refused")
	}
	if _, err := resolver.ResolveSecret(resolverProject, apphost.AppSecretRef(resolverProject, "a", "K")); err == nil {
		t.Error("a secret with no vault must be refused")
	}
}
