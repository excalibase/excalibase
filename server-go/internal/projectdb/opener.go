// Package projectdb is the one way the control plane opens a tenant's own
// database. The schema browser, the migration service and the restore probe
// all reach a project the same way — the excalibase_app credentials filed in
// vault under the project's id — and this package holds that composition so
// callers that need a project pool (deploy-time schema application, cron
// sync, the function scheduler) do not each grow their own copy.
package projectdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"

	_ "github.com/lib/pq"
)

// appRole is the non-superuser role every tenant connection is made as. It
// holds CREATE on the database, so DDL works, without the OS-level escapes
// a superuser would bring.
const appRole = "excalibase_app"

// ErrNotServable is returned for a project the platform must not serve: a
// teardown or an unconfirmed restore owns it, so its database is either
// going away or has not been proved usable.
var ErrNotServable = errors.New("project is not servable")

// Overrides point the tenant connection somewhere other than the address
// filed in vault — a local port-forward during development.
type Overrides struct {
	Host    string
	Port    string
	SSLMode string
}

// OverridesFromEnv reads the SCHEMA_DB_* knobs the schema browser and the
// migration service already honour, so every path reaches a project through
// the same address.
func OverridesFromEnv() Overrides {
	return Overrides{
		Host:    os.Getenv("SCHEMA_DB_HOST"),
		Port:    os.Getenv("SCHEMA_DB_PORT"),
		SSLMode: os.Getenv("SCHEMA_DB_SSLMODE"),
	}
}

// dsnField is one key=value pair of a lib/pq connection string. The value is
// always quoted and escaped, so a credential carrying a space or a quote is
// carried as data rather than ending the field it sits in.
func dsnField(key, value string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value)
	return key + "='" + escaped + "'"
}

// ErrTransportSecurityUnstated is returned when a host override names no
// transport security mode. An override is a port-forward and usually has no
// TLS, but inferring that would silently downgrade a connection carrying a
// tenant's credentials, so the mode has to be stated.
var ErrTransportSecurityUnstated = errors.New("a host override must state its ssl mode")

// DSNFor composes a lib/pq connection string from vault credentials.
func DSNFor(creds map[string]string, o Overrides) (string, error) {
	host, port := creds["host"], creds["port"]
	if o.Host != "" {
		host = o.Host
	}
	if o.Port != "" {
		port = o.Port
	}
	sslmode := o.SSLMode
	if sslmode == "" {
		if o.Host != "" {
			return "", ErrTransportSecurityUnstated
		}
		sslmode = "require"
	}
	fields := []string{
		dsnField("host", host),
		dsnField("port", port),
		dsnField("user", creds["username"]),
		dsnField("password", creds["password"]),
		dsnField("dbname", creds["database"]),
		dsnField("sslmode", sslmode),
	}
	return strings.Join(fields, " "), nil
}

// DSN is DSNFor for the callers that have already established their
// overrides are complete; an unstated mode yields an empty string rather
// than a downgraded connection.
func DSN(creds map[string]string, o Overrides) string {
	dsn, err := DSNFor(creds, o)
	if err != nil {
		return ""
	}
	return dsn
}

// PoolLimits bound what the cache costs. A pool holds connections on the
// tenant's database (whose own max_connections the platform does not own)
// and file descriptors here, so both the size of a pool and the number of
// cached pools are capped.
type PoolLimits struct {
	// MaxOpenConns is the per-project pool size. Default 2: the sweep's
	// claim transaction plus one concurrent statement.
	MaxOpenConns int
	// MaxPools caps how many project pools are cached at once; the least
	// recently used one is closed when a new project needs a slot.
	MaxPools int
	// ConnMaxIdleTime and ConnMaxLifetime recycle connections so a pool for
	// a project nobody touches stops holding a server-side session.
	ConnMaxIdleTime time.Duration
	ConnMaxLifetime time.Duration
	// StatementTimeout and LockTimeout bound every statement this pool runs,
	// server-side. A client-side deadline only abandons the statement — the
	// tenant's server keeps running it and keeps the connection — so the
	// bound the tenant cannot outlast has to be set on the session.
	StatementTimeout time.Duration
	LockTimeout      time.Duration
}

// Defaults chosen so one replica's worst case stays small: 64 pools x 2
// connections = 128 connections and file descriptors, against a tenant
// database whose default max_connections is 100 and which sees at most
// MaxOpenConns from this replica.
const (
	defaultMaxOpenConns    = 2
	defaultMaxPools        = 64
	defaultConnMaxIdleTime = 5 * time.Minute
	defaultConnMaxLifetime = 30 * time.Minute
	// A sweep statement that has not answered in 30s is a tenant holding the
	// platform, not a query; a lock not granted in 5s is one being held.
	defaultStatementTimeout = 30 * time.Second
	defaultLockTimeout      = 5 * time.Second
)

func (l PoolLimits) withDefaults() PoolLimits {
	if l.MaxOpenConns <= 0 {
		l.MaxOpenConns = defaultMaxOpenConns
	}
	if l.MaxPools <= 0 {
		l.MaxPools = defaultMaxPools
	}
	if l.ConnMaxIdleTime <= 0 {
		l.ConnMaxIdleTime = defaultConnMaxIdleTime
	}
	if l.ConnMaxLifetime <= 0 {
		l.ConnMaxLifetime = defaultConnMaxLifetime
	}
	if l.StatementTimeout <= 0 {
		l.StatementTimeout = defaultStatementTimeout
	}
	if l.LockTimeout <= 0 {
		l.LockTimeout = defaultLockTimeout
	}
	return l
}

// poolDSN composes the connection string a cached pool is opened with: the
// vault address plus the session bounds every statement on it runs under.
func poolDSN(creds map[string]string, o Overrides, l PoolLimits) (string, error) {
	base, err := DSNFor(creds, o)
	if err != nil {
		return "", err
	}
	options := fmt.Sprintf("-c statement_timeout=%d -c lock_timeout=%d",
		l.StatementTimeout.Milliseconds(), l.LockTimeout.Milliseconds())
	return base + " " + dsnField("options", options), nil
}

// Opener hands out a pool per managed project, keeping one pool per project
// for the life of the process. Opening is gated on the project's row: an
// unknown project has no database to open and a project that may not be
// served must not be reached at all.
type Opener struct {
	instances storage.InstanceStore
	vault     vaultclient.VaultClient
	overrides Overrides
	limits    PoolLimits
	now       func() time.Time

	mu    sync.Mutex
	pools map[string]*pool
}

// pool is one project's cached handle plus when it was last handed out, so
// the cache can evict the least recently used one.
type pool struct {
	db       *sql.DB
	lastUsed time.Time
}

func NewOpener(
	instances storage.InstanceStore,
	vault vaultclient.VaultClient,
	overrides Overrides,
	limits PoolLimits,
) *Opener {
	return &Opener{
		instances: instances,
		vault:     vault,
		overrides: overrides,
		limits:    limits.withDefaults(),
		now:       time.Now,
		pools:     make(map[string]*pool),
	}
}

// Open returns the project's pool, opening it on first use.
func (o *Opener) Open(_ context.Context, projectID string) (*sql.DB, error) {
	if err := o.checkServable(projectID); err != nil {
		return nil, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if p, ok := o.pools[projectID]; ok {
		p.lastUsed = o.now()
		return p.db, nil
	}
	creds, err := o.vault.Get(fmt.Sprintf("projects/%s/credentials/%s", projectID, appRole))
	if err != nil {
		return nil, fmt.Errorf("read %s credentials for %s: %w", appRole, projectID, err)
	}
	dsn, err := poolDSN(creds, o.overrides, o.limits)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database for %s: %w", projectID, err)
	}
	db.SetMaxOpenConns(o.limits.MaxOpenConns)
	db.SetMaxIdleConns(1)
	db.SetConnMaxIdleTime(o.limits.ConnMaxIdleTime)
	db.SetConnMaxLifetime(o.limits.ConnMaxLifetime)
	o.evictOldestLocked()
	o.pools[projectID] = &pool{db: db, lastUsed: o.now()}
	return db, nil
}

// evictOldestLocked closes the least recently used pool when the cache is
// full. Caller holds the lock.
func (o *Opener) evictOldestLocked() {
	for len(o.pools) >= o.limits.MaxPools {
		oldestID := ""
		var oldest time.Time
		for id, p := range o.pools {
			if oldestID == "" || p.lastUsed.Before(oldest) {
				oldestID, oldest = id, p.lastUsed
			}
		}
		if oldestID == "" {
			return
		}
		_ = o.pools[oldestID].db.Close()
		delete(o.pools, oldestID)
	}
}

// CachedPools reports how many project pools are held. Tests and operators
// use it to see the cache stays bounded.
func (o *Opener) CachedPools() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.pools)
}

// Evict closes and drops a project's pool. Called when the project stops
// being one the platform may reach.
func (o *Opener) Evict(projectID string) {
	o.mu.Lock()
	p, ok := o.pools[projectID]
	delete(o.pools, projectID)
	o.mu.Unlock()
	if ok {
		_ = p.db.Close()
	}
}

// ProjectDeleting is the deletion-observer hook: a project claimed for
// teardown must not keep an open pool against a database that is going away.
func (o *Opener) ProjectDeleting(projectID string) {
	o.Evict(projectID)
}

// ProjectStatusChanged is the lifecycle-observer hook. Teardown is not the
// only way a database stops being reachable: a pause takes it down for as
// long as the project stays paused, and holding the pool across that pins
// connections on a database that is not running.
func (o *Opener) ProjectStatusChanged(projectID, status string) {
	if domain.IsNotServable(status) ||
		status == string(domain.StatusPausing) ||
		status == string(domain.StatusPaused) {
		o.Evict(projectID)
	}
}

// checkServable refuses a project that has no row or that the platform must
// not serve.
func (o *Opener) checkServable(projectID string) error {
	if o.instances == nil {
		return errors.New("no instance store configured")
	}
	inst, err := o.instances.FindByProjectID(projectID)
	if err != nil {
		return fmt.Errorf("read project %s: %w", projectID, err)
	}
	if inst == nil {
		return fmt.Errorf("unknown project %s", projectID)
	}
	// Allow-list, not deny-list: a paused or still-provisioning project has
	// no database to reach, and a deny-list would let every future status
	// through by default.
	if !domain.IsActive(inst.Status) {
		return fmt.Errorf("%w: %s is %s", ErrNotServable, projectID, inst.Status)
	}
	return nil
}

// ServableProjectIDs lists the projects whose databases may be reached now.
// Callers that sweep every project (the function scheduler) use it so a
// project under teardown is never touched.
func (o *Opener) ServableProjectIDs(_ context.Context) ([]string, error) {
	if o.instances == nil {
		return nil, errors.New("no instance store configured")
	}
	insts, err := o.instances.FindAll()
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	ids := make([]string, 0, len(insts))
	for _, inst := range insts {
		if inst == nil || !domain.IsActive(inst.Status) {
			continue
		}
		ids = append(ids, inst.ProjectID)
	}
	return ids, nil
}

// Close releases every pool the opener holds. Called at shutdown.
func (o *Opener) Close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	for id, p := range o.pools {
		_ = p.db.Close()
		delete(o.pools, id)
	}
}
