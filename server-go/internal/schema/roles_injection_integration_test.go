//go:build integration

package schema

import (
	"context"
	"database/sql"
	"net/url"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestIntegration_CreateRoleStoresAHostilePasswordVerbatim(t *testing.T) {
	ctx := context.Background()
	pg, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("superuser"),
		postgres.WithPassword("superpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(ctx) })
	host, _ := pg.Host(ctx)
	port, _ := pg.MappedPort(ctx, "5432/tcp")
	dsn := func(user, password string) string {
		u := url.URL{Scheme: "postgres", User: url.UserPassword(user, password),
			Host: host + ":" + port.Port(), Path: "testdb", RawQuery: "sslmode=disable"}
		return u.String()
	}

	superDB, err := sql.Open("postgres", dsn("superuser", "superpass"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = superDB.Close() })
	superDB.SetMaxOpenConns(1)
	if _, err := superDB.ExecContext(ctx, "SET standard_conforming_strings = off"); err != nil {
		t.Fatalf("set standard_conforming_strings: %v", err)
	}

	password := `\'; CREATE ROLE pwned SUPERUSER; -- $$ '' \\`
	if err := NewIntrospector().CreateRole(ctx, superDB, CreateRoleRequest{
		Name: "hostile_pw", Password: &password, Login: true,
	}); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	var pwned int
	if err := superDB.QueryRowContext(ctx, `SELECT count(*) FROM pg_roles WHERE rolname = 'pwned'`).Scan(&pwned); err != nil {
		t.Fatalf("count: %v", err)
	}
	if pwned != 0 {
		t.Fatal("the password ran as SQL")
	}

	roleDB, err := sql.Open("postgres", dsn("hostile_pw", password))
	if err != nil {
		t.Fatalf("open as role: %v", err)
	}
	t.Cleanup(func() { _ = roleDB.Close() })
	if err := roleDB.PingContext(ctx); err != nil {
		t.Fatalf("role cannot log in with exactly the requested password: %v", err)
	}
}
