package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

// PgDog fronts every tenant database behind one shared address and
// authenticates clients against the (user, database) pairs provisioning
// writes to pgdog_users. The rows therefore decide which credential can
// reach a tenant through the pooler, so only the least-privileged
// engine-facing roles may ever be registered — never the CNPG owner or the
// docker-mode superuser.

func TestPgDogNotifier_RegisterCluster_RegistersOnlyRoutableRoles(t *testing.T) {
	store := &fakePgDogStore{}
	n, _ := NewPgDogNotifier(store, "")

	roles := []PgDogRole{
		{Name: "excalibase_app", Password: testutil.FixtureSecret("pgdog-app")},
		{Name: "auth_admin", Password: testutil.FixtureSecret("pgdog-auth")},
	}
	if err := n.RegisterCluster(context.Background(), "proj-1", "ns-1", "app", roles); err != nil {
		t.Fatalf("RegisterCluster: %v", err)
	}
	if len(store.users) != 2 {
		t.Fatalf("expected 2 pgdog users, got %d", len(store.users))
	}
	for i, want := range roles {
		got := store.users[i]
		if got.Name != want.Name || got.Password != want.Password {
			t.Errorf("user %d: got %s/%q want %s/%q", i, got.Name, got.Password, want.Name, want.Password)
		}
		if got.Database != "proj-1" {
			t.Errorf("user %s must be scoped to logical database proj-1, got %q", got.Name, got.Database)
		}
	}
}

func TestPgDogNotifier_RegisterCluster_RejectsNonRoutableRoles(t *testing.T) {
	for _, name := range []string{"postgres", "app", "cdc_watcher", "streaming_replica", ""} {
		t.Run("role="+name, func(t *testing.T) {
			store := &fakePgDogStore{}
			n, _ := NewPgDogNotifier(store, "")

			err := n.RegisterCluster(context.Background(), "proj-1", "ns-1", "app",
				[]PgDogRole{{Name: name, Password: "x"}})
			if !errors.Is(err, ErrPgDogRoleNotRoutable) {
				t.Fatalf("expected ErrPgDogRoleNotRoutable, got %v", err)
			}
			if len(store.users) != 0 || len(store.databases) != 0 {
				t.Errorf("nothing may be written when a role is rejected: users=%d databases=%d",
					len(store.users), len(store.databases))
			}
		})
	}
}

func TestPgDogNotifier_RegisterCluster_RejectsEmptyRoleSet(t *testing.T) {
	store := &fakePgDogStore{}
	n, _ := NewPgDogNotifier(store, "")

	err := n.RegisterCluster(context.Background(), "proj-1", "ns-1", "app", nil)
	if !errors.Is(err, ErrPgDogRoleNotRoutable) {
		t.Fatalf("expected ErrPgDogRoleNotRoutable for empty role set, got %v", err)
	}
	if len(store.databases) != 0 {
		t.Error("a database route without any user must not be written")
	}
}

func TestPgDogNotifier_RegisterCluster_RejectsMixedRoleSetBeforeWriting(t *testing.T) {
	store := &fakePgDogStore{}
	n, _ := NewPgDogNotifier(store, "")

	err := n.RegisterCluster(context.Background(), "proj-1", "ns-1", "app", []PgDogRole{
		{Name: "excalibase_app", Password: "ok"},
		{Name: "postgres", Password: "never"},
	})
	if !errors.Is(err, ErrPgDogRoleNotRoutable) {
		t.Fatalf("expected ErrPgDogRoleNotRoutable, got %v", err)
	}
	if len(store.users) != 0 {
		t.Errorf("validation must run before any write; got %d users", len(store.users))
	}
}

func TestPgDogNotifier_DeregisterCluster_RemovesEveryUserOfTheProject(t *testing.T) {
	store := &fakePgDogStore{}
	n, _ := NewPgDogNotifier(store, "")

	if err := n.DeregisterCluster(context.Background(), "proj-1"); err != nil {
		t.Fatalf("DeregisterCluster: %v", err)
	}
	if len(store.removedUserDatabases) != 1 || store.removedUserDatabases[0] != "proj-1" {
		t.Errorf("expected users removed by database proj-1, got %v", store.removedUserDatabases)
	}
	if len(store.removedDatabases) != 1 || store.removedDatabases[0] != "proj-1" {
		t.Errorf("expected database proj-1 removed, got %v", store.removedDatabases)
	}
}

// Full provisioning run: the roles PgDog learns about must be the generated
// engine roles with the same passwords the vault holds, and the CNPG owner
// credential returned by the provisioner must never appear.
func TestProvision_RegistersEngineRolesWithPgDog(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	v := newFakeVault()
	svc.SetVault(v)
	store := &fakePgDogStore{}
	n, _ := NewPgDogNotifier(store, "")
	svc.SetPgDogNotifier(n)

	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		PostgresVersion: "17",
		ProjectName:     "pgdog-roles",
		OrgID:           "org1",
		DBType:          domain.PostgreSQL,
	})
	if err != nil {
		t.Fatalf(testUnexpErrFmt, err)
	}
	if resp.Status != "ACTIVE" {
		t.Fatalf("status: got %s want ACTIVE", resp.Status)
	}

	byName := map[string]domain.PgDogUser{}
	for _, u := range store.users {
		byName[u.Name] = u
	}
	if _, leaked := byName["app"]; leaked {
		t.Error("CNPG owner credential must never be registered with PgDog")
	}
	for _, role := range []string{"excalibase_app", "auth_admin"} {
		u, ok := byName[role]
		if !ok {
			t.Errorf("role %s not registered with PgDog; registered=%v", role, store.users)
			continue
		}
		if u.Database != resp.ProjectID {
			t.Errorf("role %s scoped to %q, want project %q", role, u.Database, resp.ProjectID)
		}
		vaulted := v.data["projects/"+resp.ProjectID+"/credentials/"+role]["password"]
		if vaulted == "" || u.Password != vaulted {
			t.Errorf("role %s: PgDog password must equal the vault password", role)
		}
	}
	if len(byName) != 2 {
		t.Errorf("expected exactly 2 routable users, got %d: %v", len(byName), store.users)
	}
	for _, d := range store.databases {
		if d.Name != resp.ProjectID {
			t.Errorf("logical database must be the project id, got %q", d.Name)
		}
	}
}

// Without a vault the engine roles are never created, so there is nothing
// safe to route — PgDog must not fall back to the owner credential.
func TestProvision_WithoutEngineRoles_RegistersNothingWithPgDog(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	store := &fakePgDogStore{}
	n, _ := NewPgDogNotifier(store, "")
	svc.SetPgDogNotifier(n)

	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		PostgresVersion: "17",
		ProjectName:     "pgdog-novault",
		OrgID:           "org1",
		DBType:          domain.PostgreSQL,
	})
	if err != nil {
		t.Fatalf(testUnexpErrFmt, err)
	}
	if resp.Status != "ACTIVE" {
		t.Fatalf("status: got %s want ACTIVE", resp.Status)
	}
	if len(store.users) != 0 || len(store.databases) != 0 {
		t.Errorf("no PgDog rows expected without engine roles: users=%v databases=%v", store.users, store.databases)
	}
}

func TestDeprovision_DeregistersPgDogByProject(t *testing.T) {
	svc, fsStore, mock := setupProvisioningTest(t)
	mock.SetupPostgreSQLMock("pgdog-del", "org1-pgdog-del", 1)
	store := &fakePgDogStore{}
	n, _ := NewPgDogNotifier(store, "")
	svc.SetPgDogNotifier(n)

	fsStore.Create(&domain.DatabaseInstance{
		ProjectID: "pgdog-del",
		OrgID:     "org1",
		DBType:    domain.PostgreSQL,
		Namespace: "org1-pgdog-del",
		Status:    "ACTIVE",
		Username:  "app",
	})
	if err := svc.Deprovision(context.Background(), "pgdog-del"); err != nil {
		t.Fatalf(testDeprovisionFmt, err)
	}
	if len(store.removedUserDatabases) != 1 || store.removedUserDatabases[0] != "pgdog-del" {
		t.Errorf("expected every pgdog user of pgdog-del removed, got %v", store.removedUserDatabases)
	}
}
