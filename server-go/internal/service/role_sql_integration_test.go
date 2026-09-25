//go:build integration

package service

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

var hostilePasswords = map[string][3]string{
	"dollar quote breakout": {
		"a$$; CREATE ROLE pwned_auth SUPERUSER LOGIN; DO $$ BEGIN",
		"b$$; CREATE ROLE pwned_app SUPERUSER LOGIN; DO $$ BEGIN",
		"c$$; CREATE ROLE pwned_watcher SUPERUSER LOGIN; DO $$ BEGIN",
	},
	"tagged dollar and quotes": {
		"$x$'; CREATE ROLE pwned_tag SUPERUSER; --$x$",
		`'); CREATE ROLE pwned_paren SUPERUSER; --`,
		"\"; DROP SCHEMA public CASCADE; --",
	},
	"backslashes and semicolons": {
		`\'; CREATE ROLE pwned_backslash SUPERUSER; --`,
		`\\\' ; ; \$$ \x27 E'\''`,
		"tab\tnewline\nend;",
	},
}

type rolePostgres struct {
	container *postgres.PostgresContainer
	host      string
	port      string
}

func startRolePostgres(t *testing.T) *rolePostgres {
	t.Helper()
	ctx := context.Background()
	pg, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("app"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("p"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(45*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(ctx) })
	host, err := pg.Host(ctx)
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	port, err := pg.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	return &rolePostgres{container: pg, host: host, port: port.Port()}
}

// psql runs sqlText exactly the way provisioning does: psql -c as postgres.
func (p *rolePostgres) psql(t *testing.T, sqlText string) {
	t.Helper()
	code, out, err := p.container.Exec(context.Background(),
		[]string{"psql", "-U", "postgres", "-d", "app", "-c", sqlText})
	if err != nil {
		t.Fatalf("exec psql: %v", err)
	}
	if code != 0 {
		body, _ := io.ReadAll(out)
		t.Fatalf("psql exit %d: %s", code, body)
	}
}

func (p *rolePostgres) connect(t *testing.T, user, password string) *sql.DB {
	t.Helper()
	dsn := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     p.host + ":" + p.port,
		Path:     "app",
		RawQuery: "sslmode=disable",
	}
	db, err := sql.Open("postgres", dsn.String())
	if err != nil {
		t.Fatalf("open as %s: %v", user, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func (p *rolePostgres) assertLogin(t *testing.T, role, password string) {
	t.Helper()
	var current string
	if err := p.connect(t, role, password).QueryRow("SELECT current_user").Scan(&current); err != nil {
		t.Fatalf("%s cannot log in with its password %q: %v", role, password, err)
	}
	if current != role {
		t.Fatalf("logged in as %s, want %s", current, role)
	}
}

func (p *rolePostgres) assertNoInjectedEffects(t *testing.T) {
	t.Helper()
	db := p.connect(t, "postgres", "p")
	var pwned, superusers int
	if err := db.QueryRow(`SELECT count(*) FROM pg_roles WHERE rolname LIKE 'pwned%'`).Scan(&pwned); err != nil {
		t.Fatalf("count pwned roles: %v", err)
	}
	if pwned != 0 {
		t.Fatalf("hostile password created %d role(s)", pwned)
	}
	if err := db.QueryRow(`SELECT count(*) FROM pg_roles WHERE rolsuper`).Scan(&superusers); err != nil {
		t.Fatalf("count superusers: %v", err)
	}
	if superusers != 1 {
		t.Fatalf("superusers = %d, want only postgres", superusers)
	}
	var publicExists bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT FROM pg_namespace WHERE nspname = 'public')`).Scan(&publicExists); err != nil || !publicExists {
		t.Fatalf("public schema gone (err=%v)", err)
	}
}

func (p *rolePostgres) assertRolePasswords(t *testing.T, passwords [3]string) {
	t.Helper()
	for i, role := range []string{roleAuthAdmin, roleApp, roleWatcher} {
		p.assertLogin(t, role, passwords[i])
	}
}

func resetAll(passwords [3]string) string {
	return BuildProjectRoleResetSQL([]RolePassword{
		{Role: roleAuthAdmin, Password: passwords[0]},
		{Role: roleApp, Password: passwords[1]},
		{Role: roleWatcher, Password: passwords[2]},
	})
}

func TestProjectRoleSQL_HostilePasswordsAreStoredVerbatimAndRunNothing(t *testing.T) {
	pg := startRolePostgres(t)

	first := hostilePasswords["dollar quote breakout"]
	pg.psql(t, BuildProjectRoleSQL(first[0], first[1], first[2], "app", "cdc_watcher_pub"))
	pg.assertNoInjectedEffects(t)
	pg.assertRolePasswords(t, first)

	for name, passwords := range hostilePasswords {
		t.Run("reset "+name, func(t *testing.T) {
			pg.psql(t, resetAll(passwords))
			pg.assertNoInjectedEffects(t)
			pg.assertRolePasswords(t, passwords)
		})
	}
}

func TestProjectRoleSQL_HostilePasswordsWithNonStandardStrings(t *testing.T) {
	pg := startRolePostgres(t)
	pg.psql(t, "ALTER DATABASE app SET standard_conforming_strings = off")
	pg.psql(t, "ALTER DATABASE app SET escape_string_warning = off")

	passwords := hostilePasswords["backslashes and semicolons"]
	pg.psql(t, BuildProjectRoleSQL(passwords[0], passwords[1], passwords[2], "app", "cdc_watcher_pub"))
	pg.assertNoInjectedEffects(t)
	pg.assertRolePasswords(t, passwords)

	for name, next := range hostilePasswords {
		t.Run("reset "+name, func(t *testing.T) {
			pg.psql(t, resetAll(next))
			pg.assertNoInjectedEffects(t)
			pg.assertRolePasswords(t, next)
		})
	}
}

func TestProjectRoleSQL_ResetAdminWithHostileName(t *testing.T) {
	pg := startRolePostgres(t)
	pg.psql(t, `CREATE ROLE "owner$$x" LOGIN PASSWORD 'old'`)
	password := fmt.Sprintf("n$$; CREATE ROLE pwned_admin SUPERUSER; --%s", `\'`)

	pg.psql(t, BuildProjectRoleResetSQL([]RolePassword{{Role: "owner$$x", Password: password}}))

	pg.assertNoInjectedEffects(t)
	pg.assertLogin(t, "owner$$x", password)
}
