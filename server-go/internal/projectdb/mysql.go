package projectdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/mysqltls"
	"github.com/excalibase/provisioning-poc/internal/schema"
)

// ErrNotMySQL is OpenMySQL's answer for a project that is not on MySQL.
var ErrNotMySQL = errors.New("not a MySQL project")

// mysqlRole is who Studio and MCP reach a MySQL project as: the customer's
// own admin login, so SQL they write can do exactly what that login can and
// never reaches the platform's auth and excalibase databases, which
// excalibase_app may (EXC-619 review).
const mysqlRole = "admin"

// OpenMySQL returns a MySQL project's pool (ADR 0041), as the admin login over
// TLS. A script pool lets one request carry several statements, for the SQL
// runner and migrations; every other caller gets a pool that refuses a
// second statement, so a read-only call cannot end its transaction.
func (o *Opener) OpenMySQL(_ context.Context, projectID string, script bool) (*sql.DB, error) {
	inst, err := o.servableInstance(projectID)
	if err != nil {
		return nil, err
	}
	if inst.DBType != domain.MySQL {
		return nil, fmt.Errorf("%w: %s", ErrNotMySQL, projectID)
	}
	key := projectID
	if script {
		key = mysqlScriptKey(projectID)
	}
	return o.cached(projectID, key, mysqlRole, func(creds map[string]string) (*sql.DB, error) {
		if !domain.IsMySQLRecord(creds) || creds["username"] != mysqlRole {
			return nil, fmt.Errorf("the %s record of %s is not its MySQL admin login", mysqlRole, projectID)
		}
		config, err := MySQLConfigFor(creds, o.overrides, o.limits, script)
		if err != nil {
			return nil, err
		}
		connector, err := mysql.NewConnector(config)
		if err != nil {
			return nil, fmt.Errorf("open database for %s: %w", projectID, err)
		}
		db := sql.OpenDB(connector)
		o.connectors[db] = connector
		return db, nil
	})
}

// KillMySQLQuery stops a statement running on one of this opener's MySQL
// pools, over a connection of its own: the pool may be full of runaways.
func (o *Opener) KillMySQLQuery(ctx context.Context, db *sql.DB, threadID int64) error {
	o.mu.Lock()
	connector, ok := o.connectors[db]
	o.mu.Unlock()
	if !ok {
		return errors.New("not a MySQL pool of this opener")
	}
	return schema.KillMySQLQuery(ctx, connector, threadID)
}

func mysqlScriptKey(projectID string) string { return projectID + "#script" }

// MySQLConfigFor is the connection a MySQL pool opens. max_execution_time
// bounds SELECTs and the lock waits bound DDL and row locks. The server is
// verified against the record's CA and host (EXC-589) whatever the ssl mode
// override says: MySQL serves TLS everywhere, and a port-forward is dialled
// while the record's name is checked.
func MySQLConfigFor(creds map[string]string, o Overrides, l PoolLimits, multiStatements bool) (*mysql.Config, error) {
	host, port := creds["host"], creds["port"]
	if o.Host != "" {
		host = o.Host
	}
	if o.Port != "" {
		port = o.Port
	}
	if o.Host != "" && o.SSLMode == "" {
		return nil, ErrTransportSecurityUnstated
	}
	config := mysql.NewConfig()
	config.Net, config.Addr = "tcp", net.JoinHostPort(host, port)
	config.User, config.Passwd, config.DBName = creds["username"], creds["password"], creds["database"]
	tlsConfig, err := mysqltls.ClientConfig(creds[domain.CredentialServerCAKey], creds["host"])
	if err != nil {
		return nil, err
	}
	config.TLS = tlsConfig
	config.MultiStatements = multiStatements
	config.Timeout = 10 * time.Second
	lockSeconds := strconv.Itoa(wholeSeconds(l.LockTimeout))
	config.Params = map[string]string{
		"max_execution_time":       strconv.FormatInt(l.StatementTimeout.Milliseconds(), 10),
		"lock_wait_timeout":        lockSeconds,
		"innodb_lock_wait_timeout": lockSeconds,
	}
	return config, nil
}

// wholeSeconds rounds up: MySQL's lock waits take whole seconds, at least one.
func wholeSeconds(d time.Duration) int {
	return max(1, int(math.Ceil(d.Seconds())))
}
