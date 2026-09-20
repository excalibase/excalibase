//go:build integration

package schema

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	_ "github.com/lib/pq"
)

// EXC-407. CREATE EXTENSION only works if the extension's files are in the
// image, so the only convincing test is the one that actually runs it: for
// every major the catalogue supports, build that major's image from the
// repository Dockerfile and install every extension on the tenant allowlist.
//
// It builds rather than pulls on purpose. Pulling would test whatever is in
// the registry today; building tests the Dockerfile in this commit, which is
// the thing a change can break.

func dockerfileContext(t *testing.T) string {
	t.Helper()
	// internal/schema -> server-go -> repository root
	path, err := filepath.Abs(filepath.Join("..", "..", "..", "charts", "excalibase-postgres"))
	if err != nil {
		t.Fatalf("resolve build context: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "Dockerfile")); err != nil {
		t.Fatalf("build context %s: %v", path, err)
	}
	return path
}

func startImageForMajor(ctx context.Context, t *testing.T, entry config.PostgresMajorEntry) *sql.DB {
	t.Helper()

	buildArgs := map[string]*string{
		"BASE_IMAGE": &entry.BaseImage,
		"PG_MAJOR":   &entry.Major,
	}
	documentDBVersion := ""
	if entry.DocumentDB {
		documentDBVersion = config.DocumentDBVersion()
	}
	buildArgs["DOCUMENTDB_VERSION"] = &documentDBVersion

	container, err := postgres.Run(ctx, "",
		testcontainers.CustomizeRequest(testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				FromDockerfile: testcontainers.FromDockerfile{
					Context:       dockerfileContext(t),
					BuildArgs:     buildArgs,
					KeepImage:     true,
					PrintBuildLog: false,
				},
				// The CNPG images carry no docker-entrypoint, so the postgres
				// module's own bootstrap does not apply: initdb and start the
				// server directly, exactly as the extension check needs.
				User: "postgres",
				Entrypoint: []string{"bash", "-c",
					// trust auth: the container is reachable only from the
					// test's own mapped port and is torn down with the test.
					"initdb -D /tmp/pgdata --auth-local=trust && " +
						// initdb only writes loopback host lines; the test connects
						// over the docker bridge.
						"echo 'host all all all trust' >> /tmp/pgdata/pg_hba.conf && " +
						"pg_ctl -D /tmp/pgdata -o '-c listen_addresses=* -p 5432' -w start && " +
						"tail -f /dev/null"},
				ExposedPorts: []string{"5432/tcp"},
				WaitingFor: wait.ForLog("database system is ready to accept connections").
					WithStartupTimeout(5 * time.Minute),
			},
		}),
		postgres.WithDatabase("postgres"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
	)
	if err != nil {
		t.Fatalf("major %s: start container: %v", entry.Major, err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("major %s: host: %v", entry.Major, err)
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatalf("major %s: port: %v", entry.Major, err)
	}

	dsn := fmt.Sprintf("host=%s port=%s user=postgres password=postgres dbname=postgres sslmode=disable", host, port.Port())
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("major %s: open: %v", entry.Major, err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for attempt := 0; attempt < 30; attempt++ {
		if err = db.PingContext(ctx); err == nil {
			return db
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("major %s: ping: %v", entry.Major, err)
	return nil
}

func TestEveryAllowlistedExtensionInstallsOnEverySupportedMajor(t *testing.T) {
	introspector := &Introspector{}

	for _, entry := range config.PostgresCatalogEntries() {
		t.Run("postgres"+entry.Major, func(t *testing.T) {
			ctx := context.Background()
			db := startImageForMajor(ctx, t, entry)

			// Sorted, not map order: CreateExtension does not CASCADE, so
			// earthdistance only installs once cube has. Alphabetical order
			// happens to satisfy that, and makes a failure reproducible.
			names := make([]string, 0, len(extensionAllowlist))
			for name := range extensionAllowlist {
				names = append(names, name)
			}
			sort.Strings(names)

			for _, name := range names {
				if err := introspector.CreateExtension(ctx, db, name, ""); err != nil {
					t.Errorf("major %s: CREATE EXTENSION %q: %v", entry.Major, name, err)
				}
			}
		})
	}
}

// DocumentDB's files have to be in the image before EXC-409 can enable it, and
// only on the majors the catalogue says can offer it. A major marked capable
// whose image lacks the files, or one marked incapable whose image has them,
// means the catalogue is lying to the API that refuses on its word.
func TestDocumentDBFilesMatchWhatTheCatalogueClaims(t *testing.T) {
	for _, entry := range config.PostgresCatalogEntries() {
		t.Run("postgres"+entry.Major, func(t *testing.T) {
			ctx := context.Background()
			db := startImageForMajor(ctx, t, entry)

			var available bool
			err := db.QueryRowContext(ctx,
				"SELECT EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'documentdb')").Scan(&available)
			if err != nil {
				t.Fatalf("major %s: query pg_available_extensions: %v", entry.Major, err)
			}
			if available != entry.DocumentDB {
				t.Errorf("major %s: catalogue says documentdb=%v, image says %v", entry.Major, entry.DocumentDB, available)
			}
		})
	}
}

// pg_cron is a DocumentDB prerequisite, not a tenant-installable extension. It
// must be present in the image and absent from the allowlist.
func TestPgCronIsInTheImageButNotOnTheAllowlist(t *testing.T) {
	if IsExtensionAllowed("pg_cron") {
		t.Fatal("pg_cron must not be tenant-installable")
	}

	for _, entry := range config.PostgresCatalogEntries() {
		t.Run("postgres"+entry.Major, func(t *testing.T) {
			ctx := context.Background()
			db := startImageForMajor(ctx, t, entry)

			var available bool
			err := db.QueryRowContext(ctx,
				"SELECT EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'pg_cron')").Scan(&available)
			if err != nil {
				t.Fatalf("major %s: query pg_available_extensions: %v", entry.Major, err)
			}
			if !available {
				t.Errorf("major %s: pg_cron is missing from the image", entry.Major)
			}
		})
	}
}
