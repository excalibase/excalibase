package provisioner

import (
	"context"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// A single host hands apps the same restricted owner login Kubernetes does
// (EXC-604): never the container's superuser.

func provisionSingleHost(t *testing.T, engine *fakeEngine, req domain.ProvisioningRequest) *ProvisioningResult {
	t.Helper()
	result, err := NewDockerPostgreSQLProvisioner(engine).Provision(context.Background(), req, testTier, func(domain.ProvisioningStage) {})
	if err != nil {
		t.Fatalf("Provision: %v (calls %v)", err, engine.calls)
	}
	return result
}

func ownerScript(t *testing.T, engine *fakeEngine) string {
	t.Helper()
	for _, script := range engine.stdin {
		if strings.Contains(script, "ALTER DATABASE") {
			return script
		}
	}
	t.Fatalf("no owner login was created; scripts %q", engine.stdin)
	return ""
}

func TestSingleHostHandsBackTheOwnerLoginNotTheSuperuser(t *testing.T) {
	for name, req := range map[string]domain.ProvisioningRequest{
		"postgres":   {ProjectName: "p1", DBType: domain.PostgreSQL, PostgresVersion: "17"},
		"documentdb": dockerDocumentDBRequest(),
	} {
		t.Run(name, func(t *testing.T) {
			engine := newFakeEngine()
			result := provisionSingleHost(t, engine, req)
			if result.Username != "app" {
				t.Fatalf("username %q, want the owner login app", result.Username)
			}
			superuserPassword := engine.specs["excalibase-p1-postgres"].Env["POSTGRES_PASSWORD"]
			if superuserPassword == "" || result.Password == superuserPassword {
				t.Fatal("the superuser password must never be handed back")
			}
			if len(result.Password) < 32 {
				t.Fatalf("owner password is %d chars", len(result.Password))
			}
			script := ownerScript(t, engine)
			for _, want := range []string{`CREATE ROLE "app" LOGIN NOSUPERUSER`, "PASSWORD 'SCRAM-SHA-256$", `ALTER DATABASE "app" OWNER TO "app"`} {
				if !strings.Contains(script, want) {
					t.Fatalf("owner script lacks %q:\n%s", want, script)
				}
			}
			if strings.Contains(script, result.Password) {
				t.Fatal("the owner script carries the plaintext password")
			}
		})
	}
}

func TestSingleHostOwnerIsTheRequestedMasterUsername(t *testing.T) {
	engine := newFakeEngine()
	result := provisionSingleHost(t, engine, domain.ProvisioningRequest{
		ProjectName: "p1", DBType: domain.PostgreSQL, PostgresVersion: "17", MasterUsername: "shop_owner", DatabaseName: "shop",
	})
	if result.Username != "shop_owner" {
		t.Fatalf("username %q", result.Username)
	}
	if !strings.Contains(ownerScript(t, engine), `ALTER DATABASE "shop" OWNER TO "shop_owner"`) {
		t.Fatal("the requested owner does not own the database")
	}
}

func TestSingleHostRefusesAPlatformRoleAsOwnerBeforeAnyContainer(t *testing.T) {
	for _, owner := range []string{"excalibase_app", "cdc_watcher", "auth_admin", "postgres", "documentdb_gateway", "Bad-Name"} {
		engine := newFakeEngine()
		_, err := NewDockerPostgreSQLProvisioner(engine).Provision(context.Background(), domain.ProvisioningRequest{
			ProjectName: "p1", DBType: domain.PostgreSQL, PostgresVersion: "17", MasterUsername: owner,
		}, testTier, func(domain.ProvisioningStage) {})
		if err == nil {
			t.Fatalf("owner %q was accepted", owner)
		}
		if len(engine.specs) != 0 {
			t.Fatalf("owner %q: a container was created before the refusal", owner)
		}
	}
}
