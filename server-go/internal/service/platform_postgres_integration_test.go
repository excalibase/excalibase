//go:build integration

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

// startPlatformPostgres boots Postgres, runs the platform migrations and returns the connected store.
func startPlatformPostgres(ctx context.Context, t *testing.T) *pgstore.Store {
	t.Helper()
	pgPwd := testutil.FixturePassword("platform-int")
	pgC, err := tcpg.Run(ctx, "postgres:16-alpine",
		tcpg.WithDatabase("platform"),
		tcpg.WithUsername("platform"),
		tcpg.WithPassword(pgPwd),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { pgC.Terminate(ctx) })

	pgHost, _ := pgC.Host(ctx)
	pgPort, _ := pgC.MappedPort(ctx, "5432/tcp")
	dsn := fmt.Sprintf("postgres://platform:%s@%s:%s/platform?sslmode=disable", pgPwd, pgHost, pgPort.Port())
	store, err := pgstore.New(dsn)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}
