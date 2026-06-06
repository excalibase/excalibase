//go:build integration

// Package pgstore provides an integration-test helper that spins up a
// throwaway Postgres container and returns a migrated *postgres.Store.
//
// It lives in its own package (rather than internal/testutil) so that the
// postgres package's own _test.go files — which import internal/testutil for
// fixture credentials — do not pull in a testutil -> postgres import cycle
// when their test binary is compiled.
package pgstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/testcontainers/testcontainers-go"
	pgmod "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// New spins up a postgres:16-alpine container, runs all migrations via
// postgres.New, and returns a ready *postgres.Store. Container and store are
// torn down via t.Cleanup. Mirrors the postgres package's internal testStore
// helper so handler tests can share one Postgres harness.
func New(t *testing.T) *postgres.Store {
	t.Helper()
	ctx := context.Background()

	containerPwd := testutil.FixturePassword("pg-container")
	pgContainer, err := pgmod.Run(ctx, "postgres:16-alpine",
		pgmod.WithDatabase("platform_test"),
		pgmod.WithUsername("platform"),
		pgmod.WithPassword(containerPwd),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() { pgContainer.Terminate(ctx) })

	host, _ := pgContainer.Host(ctx)
	port, _ := pgContainer.MappedPort(ctx, "5432/tcp")

	dsn := fmt.Sprintf("postgres://platform:%s@%s:%s/platform_test?sslmode=disable", containerPwd, host, port.Port())

	store, err := postgres.New(dsn)
	if err != nil {
		t.Fatalf("New postgres store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}
