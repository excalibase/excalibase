//go:build integration

package projectdb

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/schema"
	"github.com/excalibase/provisioning-poc/internal/testutil/testca"
)

// provisionedMySQL starts MySQL 8.4 with the platform's databases and users
// exactly as provisioning creates them, presenting a certificate from a
// project CA the vault records carry (EXC-589), and an opener over them.
func provisionedMySQL(t *testing.T) *Opener {
	t.Helper()
	ctx := context.Background()
	ca := testca.New(t, "proj_my MySQL CA")
	server := ca.Issue(t, "localhost")
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "mysql:8.4",
			Env:          map[string]string{"MYSQL_ROOT_PASSWORD": "root-pw", "MYSQL_DATABASE": "app"},
			ExposedPorts: []string{"3306/tcp"},
			Files: []testcontainers.ContainerFile{
				{Reader: strings.NewReader(server.CertPEM), ContainerFilePath: "/etc/mysql-tls/tls.crt", FileMode: 0o644},
				{Reader: strings.NewReader(server.KeyPEM), ContainerFilePath: "/etc/mysql-tls/tls.key", FileMode: 0o644},
			},
			Cmd:        []string{"--ssl-cert=/etc/mysql-tls/tls.crt", "--ssl-key=/etc/mysql-tls/tls.key"},
			WaitingFor: wait.ForLog("port: 3306  MySQL Community Server").WithStartupTimeout(3 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start mysql: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	host := "localhost"
	port, _ := container.MappedPort(ctx, "3306/tcp")
	config := mysql.NewConfig()
	config.Net, config.Addr, config.User, config.Passwd = "tcp", host+":"+port.Port(), "root", "root-pw"
	config.MultiStatements = true
	connector, err := mysql.NewConnector(config)
	if err != nil {
		t.Fatal(err)
	}
	root := sql.OpenDB(connector)
	defer root.Close()
	setup := provisioner.MySQLPlatformUsersSQL("app", provisioner.MySQLPasswords{Admin: "admin-pw", App: "app-pw", AuthAdmin: "auth-pw", Watcher: "cdc-pw"}) +
		"CREATE TABLE auth.users (id int PRIMARY KEY); CREATE TABLE excalibase.platform_state (id int PRIMARY KEY);" +
		// With binary logging on, a login without SUPER creates a trigger only when this is set.
		"SET GLOBAL log_bin_trust_function_creators = 1;"
	if _, err := root.ExecContext(ctx, setup); err != nil {
		t.Fatalf("platform users: %v", err)
	}
	record := func(user, password string) map[string]string {
		return map[string]string{"engine": "mysql", "tls": "required", "host": host, "port": port.Port(),
			"username": user, "password": password, "database": "app", "sslrootcert": ca.PEM}
	}
	vault := fakeVault{data: map[string]map[string]string{
		"projects/proj_my/credentials/admin":          record("admin", "admin-pw"),
		"projects/proj_my/credentials/excalibase_app": record("excalibase_app", "app-pw"),
	}}
	o := NewOpener(testStore(t, &domain.DatabaseInstance{ProjectID: "proj_my", Status: "ACTIVE", DBType: domain.MySQL}),
		vault, Overrides{}, PoolLimits{})
	t.Cleanup(o.Close)
	return o
}

// EXC-619 review: SQL a Developer writes runs as the customer's admin login,
// so it cannot read end users, write the platform's database, or leave a
// trigger that runs with more rights than admin's.
func TestIntegration_MySQLRunnerRunsAsTheProjectsAdmin(t *testing.T) {
	o := provisionedMySQL(t)
	ctx := context.Background()
	script, err := o.OpenMySQL(ctx, "proj_my", true)
	if err != nil {
		t.Fatal(err)
	}
	runner := schema.NewMySQLCatalog().WithKiller(o.KillMySQLQuery)
	for _, refused := range []string{
		"SELECT * FROM auth.users",
		"INSERT INTO excalibase.platform_state VALUES (1)",
		"CREATE DEFINER = 'excalibase_app'@'%' TRIGGER sneaky BEFORE INSERT ON excalibase.platform_state FOR EACH ROW SET NEW.id = NEW.id",
	} {
		if result := runner.ExecuteQuery(ctx, script, refused); result.Error == "" {
			t.Errorf("%q ran", refused)
		}
	}
	created := runner.ExecuteQuery(ctx, script,
		"CREATE TABLE notes (id int PRIMARY KEY); CREATE TRIGGER notes_bi BEFORE INSERT ON notes FOR EACH ROW SET NEW.id = NEW.id")
	if created.Error != "" {
		t.Fatal(created.Error)
	}
	definer := runner.ExecuteQuery(ctx, script, "SELECT definer FROM information_schema.triggers WHERE trigger_name = 'notes_bi'")
	if len(definer.Rows) != 1 || !strings.HasPrefix(definer.Rows[0][0].(string), "admin@") {
		t.Fatalf("definer: %+v", definer)
	}
	single, err := o.OpenMySQL(ctx, "proj_my", false)
	if err != nil {
		t.Fatal(err)
	}
	if read := runner.ExecuteReadOnlyQuery(ctx, single, "SELECT * FROM auth.users"); read.Error == "" {
		t.Error("read-only SQL read the end users")
	}
}
