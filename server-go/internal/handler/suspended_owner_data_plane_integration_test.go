//go:build integration

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/orgguard"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	pgtest "github.com/excalibase/provisioning-poc/internal/testutil/pgstore"
	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/go-chi/chi/v5"
)

const (
	suspendedOwnerID   = "owner-suspended"
	suspendedOrgID     = "7c1f0c2e-1b7a-4e0e-9d55-6a2f3f0a9b11"
	suspendedProjectID = "proj-suspended1"
)

// seedProjectOfOwner files one ACTIVE project in an org its owner owns.
func seedProjectOfOwner(t *testing.T, store *pgstore.Store) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateUser(ctx, &domain.User{
		ID: suspendedOwnerID, Username: testutil.FixtureToken("abuser"), Email: "abuser@test.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true,
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := store.CreateOrg(ctx, &domain.Org{
		ID: suspendedOrgID, Name: "Abuse Co", Slug: "abuse-co", Tier: domain.Free, OwnerID: suspendedOwnerID,
	}); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: suspendedProjectID, ProjectName: "phish", OrgID: suspendedOrgID,
		OwnerID: suspendedOwnerID, Status: "ACTIVE",
	}); err != nil {
		t.Fatalf("Create instance: %v", err)
	}
}

// suspensionGuard reads suspensions the way cmd/server's newOrgGuard does,
// with no cache so a test sees each change at once.
func suspensionGuard(store *pgstore.Store) *orgguard.Guard {
	console := pgstore.NewAdminConsole(store.DB())
	return orgguard.New(store, console.OwnerSuspendedOrgs).WithSuspendedUsers(console.SuspendedUsers).WithServingTTL(0)
}

// suspendOwner does what the admin console's Suspend writes.
func suspendOwner(t *testing.T, store *pgstore.Store) {
	t.Helper()
	err := pgstore.NewAdminConsole(store.DB()).Suspend(context.Background(), adminconsole.Suspension{
		UserID: suspendedOwnerID, Email: "abuser@test.com", Reason: "phishing pages",
		Code: adminconsole.ReasonSpamOrPhishing, At: time.Now(),
	}, nil)
	if err != nil {
		t.Fatalf("Suspend: %v", err)
	}
}

// The auth service (sign-in, sign-up, MFA, OAuth) and the engine's CORS read
// a project's /info. Once its owner is abuse-suspended the project must stop
// reading as servable, as it does for one under teardown.
func TestProjectInfoRefusedOnceOwnerIsSuspended(t *testing.T) {
	store := pgtest.New(t)
	seedProjectOfOwner(t, store)

	h := NewProvisioningHandler(service.NewProvisioningService(store, provisioner.NewFactory(), nil), store)
	h.SetCorsStore(store)
	h.SetServingGuard(suspensionGuard(store))
	r := chi.NewRouter()
	r.Get("/api/projects/{projectId}/info", h.GetProjectInfo)

	get := func() int {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/projects/"+suspendedProjectID+"/info", nil))
		return w.Code
	}
	if code := get(); code != http.StatusOK {
		t.Fatalf("precondition: /info before suspension = %d, want 200", code)
	}
	suspendOwner(t, store)
	if code := get(); code != http.StatusNotFound {
		t.Fatalf("/info after the owner was suspended = %d, want 404", code)
	}
	unsuspendOwner(t, store)
	if code := get(); code != http.StatusOK {
		t.Fatalf("/info after the unsuspend = %d, want 200: serving must come back", code)
	}
}

// The Java engine (REST, GraphQL, realtime) and the auth service open a
// project's database only through this vault read. Handing it out after the
// owner is suspended keeps the project's data plane serving.
func TestVaultDatabaseCredentialsRefusedOnceOwnerIsSuspended(t *testing.T) {
	store := pgtest.New(t)
	seedProjectOfOwner(t, store)

	v, err := vault.NewWithStore(vault.NewMemoryStore())
	if err != nil {
		t.Fatalf("new vault: %v", err)
	}
	if _, err := v.Init(1, 1); err != nil {
		t.Fatalf("init vault: %v", err)
	}
	secretPath := "projects/" + suspendedProjectID + "/credentials/excalibase_app"
	if err := v.Put(secretPath, map[string]string{"password": "pw"}); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	h := NewVaultHandler(v)
	h.SetInstanceStore(store)
	h.SetOrgStore(store)
	h.SetServingGuard(suspensionGuard(store))
	r := chi.NewRouter()
	r.Get("/api/vault/secrets/*", h.GetSecret)

	get := func() int {
		req := httptest.NewRequest(http.MethodGet, "/api/vault/secrets/"+secretPath, nil)
		ctx := auth.SetUser(req.Context(), &domain.User{ID: "svc-graphql", Role: "platform_admin", Active: true})
		ctx = auth.SetToken(ctx, serviceVaultToken())
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req.WithContext(ctx))
		return w.Code
	}
	if code := get(); code != http.StatusOK {
		t.Fatalf("precondition: credential read before suspension = %d, want 200", code)
	}
	suspendOwner(t, store)
	if code := get(); code != http.StatusNotFound {
		t.Fatalf("credential read after the owner was suspended = %d, want 404", code)
	}
	unsuspendOwner(t, store)
	if code := get(); code != http.StatusOK {
		t.Fatalf("credential read after the unsuspend = %d, want 200: serving must come back", code)
	}
}

func unsuspendOwner(t *testing.T, store *pgstore.Store) {
	t.Helper()
	if err := pgstore.NewAdminConsole(store.DB()).Unsuspend(context.Background(), suspendedOwnerID); err != nil {
		t.Fatalf("Unsuspend: %v", err)
	}
}

// A suspended account that only created a project in a good-standing org must
// not take that org's project down; the org owner's suspension does.
func TestProjectInfo_KeysOnTheOrgOwnerNotTheCreator(t *testing.T) {
	store := pgtest.New(t)
	ctx := context.Background()
	for _, id := range []string{"acme-owner", "bob"} {
		if err := store.CreateUser(ctx, &domain.User{ID: id, Username: testutil.FixtureToken(id), Email: id + "@test.com",
			PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true}); err != nil {
			t.Fatalf("CreateUser %s: %v", id, err)
		}
	}
	if err := store.CreateOrg(ctx, &domain.Org{ID: "7c1f0c2e-1b7a-4e0e-9d55-6a2f3f0a9b22", Name: "Acme", Slug: "acme", Tier: domain.Free, OwnerID: "acme-owner"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(&domain.DatabaseInstance{ProjectID: "proj-acme1", ProjectName: "site", OrgID: "7c1f0c2e-1b7a-4e0e-9d55-6a2f3f0a9b22", OwnerID: "bob", Status: "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	h := NewProvisioningHandler(service.NewProvisioningService(store, provisioner.NewFactory(), nil), store)
	h.SetCorsStore(store)
	h.SetServingGuard(suspensionGuard(store))
	r := chi.NewRouter()
	r.Get("/api/projects/{projectId}/info", h.GetProjectInfo)
	get := func() int {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/projects/proj-acme1/info", nil))
		return w.Code
	}
	suspend := func(id string) {
		if err := pgstore.NewAdminConsole(store.DB()).Suspend(ctx, adminconsole.Suspension{UserID: id, Email: id + "@test.com", Reason: "x",
			Code: adminconsole.ReasonSpamOrPhishing, At: time.Now()}, nil); err != nil {
			t.Fatal(err)
		}
	}
	suspend("bob")
	if code := get(); code != http.StatusOK {
		t.Fatalf("/info after the creator was suspended = %d, want 200: the org is in good standing", code)
	}
	suspend("acme-owner")
	if code := get(); code != http.StatusNotFound {
		t.Fatalf("/info after the org owner was suspended = %d, want 404", code)
	}
}
