//go:build integration

package apphost_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
)

// Exercises PostgresAppStore against a real Postgres. Also proves migration
// 000040 applies, since pgstore.New runs migrations on connect.

// newPGPlatformDB starts a throwaway Postgres, runs the platform migrations
// through pgstore.New and returns the migrated database. One container is
// shared by every test in the package: the migrations are the slow part and
// each test namespaces itself by project id.
var (
	sharedDBOnce sync.Once
	sharedDB     *sql.DB
	sharedDBErr  error
)

func newPGAppStore(t *testing.T) *apphost.PostgresAppStore {
	t.Helper()
	sharedDBOnce.Do(func() { sharedDB, sharedDBErr = startPlatformDB() })
	if sharedDBErr != nil {
		t.Skipf("docker unavailable, skipping integration test: %v", sharedDBErr)
	}
	return apphost.NewPostgresAppStore(sharedDB)
}

func startPlatformDB() (*sql.DB, error) {
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("platform_test"),
		postgres.WithUsername("platform"),
		postgres.WithPassword("apphost-itest"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		return nil, err
	}
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "5432/tcp")
	dsn := fmt.Sprintf("postgres://platform:apphost-itest@%s:%s/platform_test?sslmode=disable",
		host, port.Port())
	store, err := pgstore.New(dsn)
	if err != nil {
		return nil, fmt.Errorf("pgstore.New (runs migrations): %w", err)
	}
	return store.DB(), nil
}

func sampleApp(projectID, id, name string) *apphost.App {
	value := "production"
	return &apphost.App{
		ID:              id,
		ProjectID:       projectID,
		Name:            name,
		Image:           "ghcr.io/acme/storefront:1.4.2",
		Env:             []apphost.EnvVar{{Name: "MODE", Kind: apphost.KindLiteral, Value: &value}},
		Port:            8080,
		HealthCheckPath: "/healthz",
		Replicas:        1,
		Tier:            domain.Standard,
		Status:          apphost.StatusCreated,
	}
}

func TestPGAppStore_CreateGetRoundTrip(t *testing.T) {
	s := newPGAppStore(t)
	app := sampleApp("proj_itest_rt", "app_rt", "storefront")

	if err := s.Create(app); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if app.Version != 1 {
		t.Errorf("first create must be version 1, got %d", app.Version)
	}
	if app.CreatedAt.IsZero() || app.UpdatedAt.IsZero() {
		t.Error("the store must stamp created_at and updated_at")
	}

	got, err := s.Get("proj_itest_rt", "app_rt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nothing for a stored app")
	}
	if got.Image != app.Image || got.Port != app.Port || got.Tier != app.Tier {
		t.Errorf("round trip changed the record: %+v", got)
	}
	if got.Status != apphost.StatusCreated {
		t.Errorf("status: got %q want %q", got.Status, apphost.StatusCreated)
	}
	if len(got.Env) != 1 || got.Env[0].Name != "MODE" || got.Env[0].Value == nil || *got.Env[0].Value != "production" {
		t.Errorf("env did not round trip: %+v", got.Env)
	}
}

// A missing app is nil with no error, so a caller distinguishes "absent" from
// "the read failed" without parsing an error string.
func TestPGAppStore_GetMissingIsNil(t *testing.T) {
	s := newPGAppStore(t)
	got, err := s.Get("proj_itest_missing", "nope")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Errorf("a missing app must read as nil, got %+v", got)
	}
}

// One app per project is a rule enforced in SQL, not a read-then-write: two
// concurrent creates must not both find the same free slot.
func TestPGAppStore_OneAppPerProject(t *testing.T) {
	s := newPGAppStore(t)
	projectID := "proj_itest_limit"

	if err := s.Create(sampleApp(projectID, "app_first", "first")); err != nil {
		t.Fatalf("first create: %v", err)
	}
	err := s.Create(sampleApp(projectID, "app_second", "second"))
	if !errors.Is(err, apphost.ErrAppLimitReached) {
		t.Fatalf("second create must report the limit, got %v", err)
	}

	apps, err := s.List(projectID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("project must hold exactly one app, got %d", len(apps))
	}
}

func TestPGAppStore_ConcurrentCreatesAdmitOne(t *testing.T) {
	s := newPGAppStore(t)
	projectID := "proj_itest_race"

	const attempts = 6
	var wg sync.WaitGroup
	errs := make([]error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = s.Create(sampleApp(projectID, fmt.Sprintf("app_r%d", i), fmt.Sprintf("racer-%d", i)))
		}(i)
	}
	wg.Wait()

	admitted := 0
	for i, err := range errs {
		switch {
		case err == nil:
			admitted++
		case errors.Is(err, apphost.ErrAppLimitReached):
		default:
			t.Fatalf("attempt %d failed for an unexpected reason: %v", i, err)
		}
	}
	if admitted != 1 {
		t.Fatalf("exactly one concurrent create may be admitted, got %d", admitted)
	}
}

// A name is unique per project so two projects may both call their app "api".
func TestPGAppStore_NameIsUniquePerProjectOnly(t *testing.T) {
	s := newPGAppStore(t)
	if err := s.Create(sampleApp("proj_itest_n1", "app_n1", "api")); err != nil {
		t.Fatalf("first project: %v", err)
	}
	if err := s.Create(sampleApp("proj_itest_n2", "app_n2", "api")); err != nil {
		t.Fatalf("a second project must be free to reuse the name: %v", err)
	}
}

// Updating replaces the env set outright: an emptied value survives, and a
// removed key actually disappears.
func TestPGAppStore_UpdateEnvRemovesAndKeepsEmptyValues(t *testing.T) {
	s := newPGAppStore(t)
	projectID := "proj_itest_env"
	keep, drop := "keep", "drop"
	app := sampleApp(projectID, "app_env", "envs")
	app.Env = []apphost.EnvVar{
		{Name: "KEEP", Kind: apphost.KindLiteral, Value: &keep},
		{Name: "DROP", Kind: apphost.KindLiteral, Value: &drop},
	}
	if err := s.Create(app); err != nil {
		t.Fatalf("Create: %v", err)
	}

	empty := ""
	app.Env = []apphost.EnvVar{{Name: "KEEP", Kind: apphost.KindLiteral, Value: &empty}}
	if err := s.Update(app); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if app.Version != 2 {
		t.Errorf("update must bump the version, got %d", app.Version)
	}

	got, err := s.Get(projectID, "app_env")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Env) != 1 {
		t.Fatalf("the removed key must be gone, got %+v", got.Env)
	}
	if got.Env[0].Name != "KEEP" {
		t.Fatalf("wrong key survived: %+v", got.Env[0])
	}
	if got.Env[0].Value == nil || *got.Env[0].Value != "" {
		t.Errorf("an emptied value must survive as empty, got %v", got.Env[0].Value)
	}
	if !got.CreatedAt.Equal(app.CreatedAt) {
		t.Errorf("update must preserve created_at: %v vs %v", got.CreatedAt, app.CreatedAt)
	}
}

// Both pointer kinds round-trip as pointers: the row never holds a secret
// value, and a reference stays the structural target it was declared as.
func TestPGAppStore_SecretRefRoundTrips(t *testing.T) {
	s := newPGAppStore(t)
	projectID := "proj_itest_secret"
	app := sampleApp(projectID, "app_secret", "secrets")
	app.Env = []apphost.EnvVar{
		{Name: "TOKEN", Kind: apphost.KindSecret, Secret: &apphost.SecretRef{
			Path: "projects/" + projectID + "/apps/app_secret/secrets", Key: "token",
		}},
		{Name: "DATABASE_URL", Kind: apphost.KindReference, Reference: &apphost.ReferenceTarget{
			SourceKind: apphost.SourceDatabase, SourceName: "store_db", Variable: "DATABASE_URL",
		}},
	}
	if err := s.Create(app); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get(projectID, "app_secret")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Env[0].Value != nil {
		t.Errorf("a secret env var must carry no value, got %q", *got.Env[0].Value)
	}
	if got.Env[0].Kind != apphost.KindSecret || got.Env[0].Secret == nil || got.Env[0].Secret.Key != "token" {
		t.Errorf("secret reference did not round trip: %+v", got.Env[0])
	}
	if got.Env[1].Kind != apphost.KindReference || got.Env[1].Reference == nil ||
		got.Env[1].Reference.SourceName != "store_db" {
		t.Errorf("source reference did not round trip: %+v", got.Env[1])
	}
}

// Zero replicas is a legal, stopped app.
func TestPGAppStore_StoresStoppedApp(t *testing.T) {
	s := newPGAppStore(t)
	app := sampleApp("proj_itest_stop", "app_stop", "stopped")
	app.Replicas = 0
	app.Status = apphost.StatusFor(0)
	if err := s.Create(app); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get("proj_itest_stop", "app_stop")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != apphost.StatusStopped || got.Replicas != 0 {
		t.Errorf("a stopped app must persist as stopped, got status=%q replicas=%d", got.Status, got.Replicas)
	}
}

func TestPGAppStore_UpdateMissingIsNotFound(t *testing.T) {
	s := newPGAppStore(t)
	app := sampleApp("proj_itest_upd", "app_absent", "absent")
	if err := s.Update(app); !errors.Is(err, apphost.ErrAppNotFound) {
		t.Fatalf("updating an absent app must report not found, got %v", err)
	}
}

func TestPGAppStore_Delete(t *testing.T) {
	s := newPGAppStore(t)
	projectID := "proj_itest_del"
	if err := s.Create(sampleApp(projectID, "app_del", "deleteme")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Delete(projectID, "app_del"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err := s.Get(projectID, "app_del")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Error("the app must be gone after Delete")
	}
	if err := s.Delete(projectID, "app_del"); !errors.Is(err, apphost.ErrAppNotFound) {
		t.Fatalf("deleting an absent app must report not found, got %v", err)
	}
	// The slot is freed, so the project may hold an app again.
	if err := s.Create(sampleApp(projectID, "app_del2", "replacement")); err != nil {
		t.Fatalf("deleting must free the project's slot: %v", err)
	}
}

// Another project's app is never readable, writable or deletable through this
// project's id, whatever app id is named.
func TestPGAppStore_ScopedToTheProject(t *testing.T) {
	s := newPGAppStore(t)
	if err := s.Create(sampleApp("proj_itest_owner", "app_owned", "owned")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get("proj_itest_intruder", "app_owned")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Error("an app must not be readable through another project's id")
	}
	if err := s.Delete("proj_itest_intruder", "app_owned"); !errors.Is(err, apphost.ErrAppNotFound) {
		t.Fatalf("cross-project delete must report not found, got %v", err)
	}
	apps, err := s.List("proj_itest_intruder")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(apps) != 0 {
		t.Errorf("another project's apps must not be listed, got %d", len(apps))
	}
}

// The store validates too: a handler is not the only way in.
func TestPGAppStore_RefusesInvalidApp(t *testing.T) {
	s := newPGAppStore(t)
	app := sampleApp("proj_itest_bad", "app_bad", "bad")
	app.Image = "nginx"
	if err := s.Create(app); err == nil {
		t.Fatal("an unparseable image reference must be refused by the store")
	}
}
