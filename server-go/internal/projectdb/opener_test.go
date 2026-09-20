package projectdb

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// fakeVault serves one project's excalibase_app credentials.
type fakeVault struct {
	data map[string]map[string]string
	err  error
}

func (v fakeVault) Get(path string) (map[string]string, error) {
	if v.err != nil {
		return nil, v.err
	}
	creds, ok := v.data[path]
	if !ok {
		return nil, errors.New("not found")
	}
	return creds, nil
}
func (fakeVault) Put(string, map[string]string) error { return nil }
func (fakeVault) Delete(string) error                 { return nil }
func (fakeVault) DeletePrefix(string) (int, error)    { return 0, nil }
func (fakeVault) List(string) ([]string, error)       { return nil, nil }
func (fakeVault) Sealed() bool                        { return false }
func (fakeVault) GetPublicKey() (string, error)       { return "", nil }

func appCreds() map[string]map[string]string {
	return map[string]map[string]string{
		"projects/proj_a/credentials/excalibase_app": {
			"host": "db.internal", "port": "5432",
			"username": "excalibase_app", "password": "pw", "database": "proj_a",
		},
	}
}

func testStore(t *testing.T, insts ...*domain.DatabaseInstance) storage.InstanceStore {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	for _, inst := range insts {
		if err := store.Create(inst); err != nil {
			t.Fatalf("create %s: %v", inst.ProjectID, err)
		}
	}
	return store
}

func TestOpen_ReturnsOnePoolPerProject(t *testing.T) {
	o := NewOpener(testStore(t, &domain.DatabaseInstance{ProjectID: "proj_a", Status: "ACTIVE"}),
		fakeVault{data: appCreds()}, Overrides{}, PoolLimits{})
	defer o.Close()

	first, err := o.Open(context.Background(), "proj_a")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	second, err := o.Open(context.Background(), "proj_a")
	if err != nil {
		t.Fatalf("open again: %v", err)
	}
	if first != second {
		t.Error("each call opened a new pool; the project's pool must be reused")
	}
}

// A project the platform must not serve must not be opened at all: its
// database is being removed or has not been proved usable.
func TestOpen_RefusesNotServableProject(t *testing.T) {
	for _, status := range []string{string(domain.StatusDeleting), string(domain.StatusRestoring)} {
		t.Run(status, func(t *testing.T) {
			o := NewOpener(testStore(t, &domain.DatabaseInstance{ProjectID: "proj_a", Status: status}),
				fakeVault{data: appCreds()}, Overrides{}, PoolLimits{})
			defer o.Close()

			_, err := o.Open(context.Background(), "proj_a")
			if !errors.Is(err, ErrNotServable) {
				t.Fatalf("err: got %v, want ErrNotServable", err)
			}
		})
	}
}

func TestOpen_UnknownProject(t *testing.T) {
	o := NewOpener(testStore(t), fakeVault{data: appCreds()}, Overrides{}, PoolLimits{})
	defer o.Close()

	if _, err := o.Open(context.Background(), "proj_missing"); err == nil {
		t.Fatal("unknown project: want an error")
	}
}

// Credentials that cannot be read are an error, never a guessed DSN.
func TestOpen_VaultFailureIsReported(t *testing.T) {
	o := NewOpener(testStore(t, &domain.DatabaseInstance{ProjectID: "proj_a", Status: "ACTIVE"}),
		fakeVault{err: errors.New("vault is sealed")}, Overrides{}, PoolLimits{})
	defer o.Close()

	_, err := o.Open(context.Background(), "proj_a")
	if err == nil || !strings.Contains(err.Error(), "sealed") {
		t.Fatalf("err: got %v, want the vault failure reported", err)
	}
}

func TestServableProjectIDs_SkipsProjectsThatMayNotBeServed(t *testing.T) {
	o := NewOpener(testStore(t,
		&domain.DatabaseInstance{ProjectID: "proj_a", Status: "ACTIVE"},
		&domain.DatabaseInstance{ProjectID: "proj_gone", Status: string(domain.StatusDeleting)},
		&domain.DatabaseInstance{ProjectID: "proj_new", Status: string(domain.StatusRestoring)},
	), fakeVault{data: appCreds()}, Overrides{}, PoolLimits{})
	defer o.Close()

	ids, err := o.ServableProjectIDs(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 1 || ids[0] != "proj_a" {
		t.Errorf("servable projects: got %v, want [proj_a]", ids)
	}
}

func TestDSN_UsesOverridesAndDefaults(t *testing.T) {
	creds := appCreds()["projects/proj_a/credentials/excalibase_app"]
	if got := DSN(creds, Overrides{}); !strings.Contains(got, "host=db.internal") ||
		!strings.Contains(got, "sslmode=require") {
		t.Errorf("default DSN: got %q", got)
	}
	// A host override is a local port-forward, which has no TLS.
	if got := DSN(creds, Overrides{Host: "127.0.0.1", Port: "15432"}); !strings.Contains(got, "host=127.0.0.1") ||
		!strings.Contains(got, "port=15432") || !strings.Contains(got, "sslmode=disable") {
		t.Errorf("override DSN: got %q", got)
	}
	if got := DSN(creds, Overrides{Host: "127.0.0.1", SSLMode: "verify-full"}); !strings.Contains(got, "sslmode=verify-full") {
		t.Errorf("explicit sslmode: got %q", got)
	}
}

// An opener with no project rows to read cannot decide whether a project
// may be served, so it refuses rather than opening anything.
func TestOpener_WithoutAnInstanceStore(t *testing.T) {
	o := NewOpener(nil, fakeVault{data: appCreds()}, Overrides{}, PoolLimits{})
	defer o.Close()

	if _, err := o.Open(context.Background(), "proj_a"); err == nil {
		t.Error("open without an instance store: want an error")
	}
	if _, err := o.ServableProjectIDs(context.Background()); err == nil {
		t.Error("list without an instance store: want an error")
	}
}

// brokenStore fails every read, standing in for a platform database that
// is momentarily unreachable.
type brokenStore struct {
	storage.InstanceStore
}

func (brokenStore) FindByProjectID(string) (*domain.DatabaseInstance, error) {
	return nil, errors.New("platform database is down")
}
func (brokenStore) FindAll() ([]*domain.DatabaseInstance, error) {
	return nil, errors.New("platform database is down")
}

func TestOpener_ReportsAnUnreadableProjectStore(t *testing.T) {
	o := NewOpener(brokenStore{}, fakeVault{data: appCreds()}, Overrides{}, PoolLimits{})
	defer o.Close()

	if _, err := o.Open(context.Background(), "proj_a"); err == nil {
		t.Error("open with an unreadable store: want an error")
	}
	if _, err := o.ServableProjectIDs(context.Background()); err == nil {
		t.Error("list with an unreadable store: want an error")
	}
}

func TestOverridesFromEnv(t *testing.T) {
	t.Setenv("SCHEMA_DB_HOST", "127.0.0.1")
	t.Setenv("SCHEMA_DB_PORT", "15432")
	t.Setenv("SCHEMA_DB_SSLMODE", "disable")

	got := OverridesFromEnv()
	if got != (Overrides{Host: "127.0.0.1", Port: "15432", SSLMode: "disable"}) {
		t.Errorf("overrides: got %+v", got)
	}
}

// A hibernated project's database is not there to answer. Sweeping it would
// mean a connection attempt that times out every tick, forever — so the gate
// is an allow-list of one status, not a list of bad ones.
func TestOpen_RefusesEverythingButActive(t *testing.T) {
	for _, status := range []string{
		string(domain.StatusPaused), string(domain.StatusPausing), string(domain.StatusResuming),
		"PROVISIONING", "FAILED", "",
	} {
		t.Run(status, func(t *testing.T) {
			o := NewOpener(testStore(t, &domain.DatabaseInstance{ProjectID: "proj_a", Status: status}),
				fakeVault{data: appCreds()}, Overrides{}, PoolLimits{})
			defer o.Close()

			if _, err := o.Open(context.Background(), "proj_a"); err == nil {
				t.Fatalf("a project in %q was opened", status)
			}
		})
	}
}

func TestServableProjectIDs_ListsOnlyActiveProjects(t *testing.T) {
	o := NewOpener(testStore(t,
		&domain.DatabaseInstance{ProjectID: "proj_live", Status: "ACTIVE"},
		&domain.DatabaseInstance{ProjectID: "proj_paused", Status: string(domain.StatusPaused)},
		&domain.DatabaseInstance{ProjectID: "proj_gone", Status: string(domain.StatusDeleting)},
	), fakeVault{data: appCreds()}, Overrides{}, PoolLimits{})
	defer o.Close()

	ids, err := o.ServableProjectIDs(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 1 || ids[0] != "proj_live" {
		t.Errorf("swept projects: got %v, want [proj_live]", ids)
	}
}

// A pool per tenant costs connections on the tenant's database and file
// descriptors here, so the cache is bounded and the least recently used pool
// is closed when a new one is opened.
func TestOpen_CachedPoolsAreBounded(t *testing.T) {
	insts := []*domain.DatabaseInstance{}
	creds := map[string]map[string]string{}
	for _, id := range []string{"proj_1", "proj_2", "proj_3"} {
		insts = append(insts, &domain.DatabaseInstance{ProjectID: id, Status: "ACTIVE"})
		creds["projects/"+id+"/credentials/excalibase_app"] = map[string]string{
			"host": "db", "port": "5432", "username": "u", "password": "p", "database": id,
		}
	}
	o := NewOpener(testStore(t, insts...), fakeVault{data: creds}, Overrides{}, PoolLimits{MaxPools: 2})
	defer o.Close()

	ctx := context.Background()
	first, _ := o.Open(ctx, "proj_1")
	if _, err := o.Open(ctx, "proj_2"); err != nil {
		t.Fatalf("open proj_2: %v", err)
	}
	if _, err := o.Open(ctx, "proj_3"); err != nil {
		t.Fatalf("open proj_3: %v", err)
	}
	if got := o.CachedPools(); got != 2 {
		t.Errorf("cached pools: got %d, want the cap of 2", got)
	}
	// proj_1 was the least recently used, so its pool was closed; opening it
	// again yields a fresh one.
	again, err := o.Open(ctx, "proj_1")
	if err != nil {
		t.Fatalf("reopen proj_1: %v", err)
	}
	if again == first {
		t.Error("the evicted pool was handed out again after being closed")
	}
}

// A project that stops being servable must not keep a live pool on a
// database that is being torn down or hibernated.
func TestProjectDeleting_EvictsThePool(t *testing.T) {
	store := testStore(t, &domain.DatabaseInstance{ProjectID: "proj_a", Status: "ACTIVE"})
	o := NewOpener(store, fakeVault{data: appCreds()}, Overrides{}, PoolLimits{})
	defer o.Close()

	if _, err := o.Open(context.Background(), "proj_a"); err != nil {
		t.Fatalf("open: %v", err)
	}
	if o.CachedPools() != 1 {
		t.Fatal("the pool was not cached")
	}
	o.ProjectDeleting("proj_a")
	if got := o.CachedPools(); got != 0 {
		t.Errorf("cached pools after teardown: got %d, want 0", got)
	}
}

// The pool a tenant gets is small and recycled: one hot project must not be
// able to hold open a large share of its own database's connections.
func TestOpen_PoolIsBounded(t *testing.T) {
	o := NewOpener(testStore(t, &domain.DatabaseInstance{ProjectID: "proj_a", Status: "ACTIVE"}),
		fakeVault{data: appCreds()}, Overrides{}, PoolLimits{MaxOpenConns: 3})
	defer o.Close()

	db, err := o.Open(context.Background(), "proj_a")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := db.Stats().MaxOpenConnections; got != 3 {
		t.Errorf("max open connections: got %d, want 3", got)
	}
}
