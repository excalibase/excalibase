package projectdb

import (
	"context"
	"crypto/x509"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/mysqltls"
	"github.com/excalibase/provisioning-poc/internal/testutil/testca"
)

var mysqlTestCA = testca.New(&testing.T{}, "proj_my MySQL CA")

func mysqlCreds() map[string]map[string]string {
	return map[string]map[string]string{
		"projects/proj_my/credentials/admin": {
			"engine": "mysql", "tls": "required", "host": "moco-proj-my-primary", "port": "3306",
			"username": "admin", "password": "pw", "database": "app", "sslrootcert": mysqlTestCA.PEM,
		},
	}
}

// verifiesAs reports whether the config accepts a certificate the project's
// CA issued for name, and refuses one another CA issued for the same name.
func verifiesAs(t *testing.T, config *mysql.Config, name string) bool {
	t.Helper()
	if config.TLS == nil || config.TLS.InsecureSkipVerify || config.TLS.ServerName != name {
		return false
	}
	verify := func(server testca.Server) error {
		_, err := server.Cert.Verify(x509.VerifyOptions{Roots: config.TLS.RootCAs, DNSName: config.TLS.ServerName})
		return err
	}
	return verify(mysqlTestCA.Issue(t, name)) == nil && verify(testca.New(t, "other").Issue(t, name)) != nil
}

// EXC-619: Studio reaches a MySQL project as the project's admin login over TLS, with
// the platform's bounds set on the session.
func TestMySQLConfigFor_BoundsTheSessionAndRequiresTLS(t *testing.T) {
	limits := PoolLimits{StatementTimeout: 30 * time.Second, LockTimeout: 5 * time.Second}
	config, err := MySQLConfigFor(mysqlCreds()["projects/proj_my/credentials/admin"], Overrides{}, limits, false)
	if err != nil {
		t.Fatal(err)
	}
	if config.Addr != "moco-proj-my-primary:3306" || config.User != "admin" || config.DBName != "app" {
		t.Fatalf("target: %+v", config)
	}
	if !verifiesAs(t, config, "moco-proj-my-primary") {
		t.Errorf("TLS does not verify the server against the project's CA: %+v", config.TLS)
	}
	if config.MultiStatements {
		t.Error("a single-statement pool allows several statements")
	}
	for name, want := range map[string]string{"max_execution_time": "30000", "lock_wait_timeout": "5", "innodb_lock_wait_timeout": "5"} {
		if config.Params[name] != want {
			t.Errorf("%s = %q, want %q", name, config.Params[name], want)
		}
	}
	script, err := MySQLConfigFor(mysqlCreds()["projects/proj_my/credentials/admin"], Overrides{}, limits, true)
	if err != nil || !script.MultiStatements {
		t.Fatalf("script pool: %+v %v", script, err)
	}
}

func TestMySQLConfigFor_AnOverrideStatesItsTransport(t *testing.T) {
	creds := mysqlCreds()["projects/proj_my/credentials/admin"]
	if _, err := MySQLConfigFor(creds, Overrides{Host: "127.0.0.1", Port: "13306"}, PoolLimits{}, false); !errors.Is(err, ErrTransportSecurityUnstated) {
		t.Fatalf("err = %v", err)
	}
	// MySQL serves TLS everywhere, so even "disable" (Postgres on a single host)
	// verifies, and a port-forward still checks the name the record dials.
	for _, mode := range []string{"disable", "require", "verify-ca", "verify-full"} {
		config, err := MySQLConfigFor(creds, Overrides{Host: "127.0.0.1", Port: "13306", SSLMode: mode}, PoolLimits{}.withDefaults(), false)
		if err != nil || config.Addr != "127.0.0.1:13306" || !verifiesAs(t, config, "moco-proj-my-primary") {
			t.Fatalf("%s: config %+v, %v", mode, config, err)
		}
	}
}

// EXC-589: a record without the project's CA opens nothing; there is no
// unverified fallback.
func TestMySQLConfigFor_RefusesARecordWithoutTheServerCA(t *testing.T) {
	creds := maps.Clone(mysqlCreds()["projects/proj_my/credentials/admin"])
	delete(creds, "sslrootcert")
	if _, err := MySQLConfigFor(creds, Overrides{}, PoolLimits{}, false); !errors.Is(err, mysqltls.ErrNoServerCA) {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenMySQL_KeepsAScriptPoolBesideTheSingleStatementOne(t *testing.T) {
	o := NewOpener(testStore(t,
		&domain.DatabaseInstance{ProjectID: "proj_my", Status: "ACTIVE", DBType: domain.MySQL},
	), fakeVault{data: mysqlCreds()}, Overrides{}, PoolLimits{})
	defer o.Close()
	single, err := o.OpenMySQL(context.Background(), "proj_my", false)
	if err != nil {
		t.Fatal(err)
	}
	script, err := o.OpenMySQL(context.Background(), "proj_my", true)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := o.OpenMySQL(context.Background(), "proj_my", false)
	if single == script || again != single || o.CachedPools() != 2 {
		t.Fatalf("pools: single %p script %p again %p cached %d", single, script, again, o.CachedPools())
	}
	o.Evict("proj_my")
	if o.CachedPools() != 0 {
		t.Fatalf("evict left %d pools", o.CachedPools())
	}
}

func TestOpenMySQL_RefusesAPostgresProject(t *testing.T) {
	o := NewOpener(testStore(t, &domain.DatabaseInstance{ProjectID: "proj_a", Status: "ACTIVE"}),
		fakeVault{data: appCreds()}, Overrides{}, PoolLimits{})
	defer o.Close()
	if _, err := o.OpenMySQL(context.Background(), "proj_a", false); !errors.Is(err, ErrNotMySQL) {
		t.Fatalf("err = %v", err)
	}
	if _, err := o.OpenMySQL(context.Background(), "proj_none", false); err == nil {
		t.Fatal("an unknown project opened")
	}
}

func TestOpenMySQL_RefusesARecordThatIsNotMySQL(t *testing.T) {
	creds := mysqlCreds()
	delete(creds["projects/proj_my/credentials/admin"], "engine")
	o := NewOpener(testStore(t, &domain.DatabaseInstance{ProjectID: "proj_my", Status: "ACTIVE", DBType: domain.MySQL}),
		fakeVault{data: creds}, Overrides{}, PoolLimits{})
	defer o.Close()
	if _, err := o.OpenMySQL(context.Background(), "proj_my", false); err == nil {
		t.Fatal("a Postgres record opened a MySQL pool")
	}
}

// EXC-619 review: customer SQL runs as the customer's own admin login, never
// as excalibase_app, which also reaches the platform's databases.
func TestOpenMySQL_RunsAsTheProjectsAdminLogin(t *testing.T) {
	appOnly := map[string]map[string]string{
		"projects/proj_my/credentials/excalibase_app": {
			"engine": "mysql", "host": "h", "port": "3306", "username": "excalibase_app", "password": "pw", "database": "app",
		},
	}
	o := NewOpener(testStore(t, &domain.DatabaseInstance{ProjectID: "proj_my", Status: "ACTIVE", DBType: domain.MySQL}),
		fakeVault{data: appOnly}, Overrides{}, PoolLimits{})
	defer o.Close()
	if _, err := o.OpenMySQL(context.Background(), "proj_my", true); err == nil {
		t.Fatal("a MySQL pool opened as excalibase_app")
	}
}

func TestKillMySQLQuery_RefusesAPoolItDidNotOpen(t *testing.T) {
	o := NewOpener(testStore(t), fakeVault{}, Overrides{}, PoolLimits{})
	defer o.Close()
	if err := o.KillMySQLQuery(context.Background(), nil, 7); err == nil {
		t.Fatal("a kill went out for an unknown pool")
	}
}
