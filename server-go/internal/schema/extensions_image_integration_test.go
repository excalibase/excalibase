//go:build integration

package schema

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
// every major the catalogue supports, install every extension on the tenant
// allowlist into that major's image, and round-trip a document through
// DocumentDB on the majors that claim it.
//
// These tests are opt-in. Building four images — three of which compile
// DocumentDB from source — takes minutes and does not belong in the ordinary
// integration run, which must stay fast and must not build container images.
// They are gated rather than deleted because they are the only proof the
// images work; the gate is explicit so it cannot quietly rot.
//
// Two ways to run them:
//
//   * Locally, building each image from the repository Dockerfile — this is
//     what proves the Dockerfile in this commit still works:
//
//       EXCALIBASE_POSTGRES_IMAGE_TESTS=1 go test -tags integration ./internal/schema/
//
//   * In the publish workflow, against one already-built image, so the thing
//     verified is the exact artefact about to be published rather than a
//     rebuild of it:
//
//       EXCALIBASE_POSTGRES_IMAGE_TESTS=1 \
//       EXCALIBASE_POSTGRES_IMAGE_MAJOR=17 \
//       EXCALIBASE_POSTGRES_IMAGE_REF=excalibase/postgresql:17 \
//       go test -tags integration ./internal/schema/

const (
	// imageTestsEnv opts in. Without it every test in this file skips.
	imageTestsEnv = "EXCALIBASE_POSTGRES_IMAGE_TESTS"
	// imageMajorEnv and imageRefEnv name one already-built image to test
	// instead of building the whole catalogue. Both or neither.
	imageMajorEnv = "EXCALIBASE_POSTGRES_IMAGE_MAJOR"
	imageRefEnv   = "EXCALIBASE_POSTGRES_IMAGE_REF"
)

// requireImageTests skips unless the caller asked for these explicitly, and
// says exactly how to ask.
func requireImageTests(t *testing.T) {
	t.Helper()
	if os.Getenv(imageTestsEnv) != "" {
		return
	}
	t.Skipf("builds container images; run it with %s=1 go test -tags integration -run %s ./internal/schema/",
		imageTestsEnv, t.Name())
}

// majorsUnderTest is the whole catalogue normally, or the single major named
// by imageMajorEnv when the workflow is verifying one built image.
func majorsUnderTest(t *testing.T) []config.PostgresMajorEntry {
	t.Helper()
	entries := config.PostgresCatalogEntries()
	pinned := strings.TrimSpace(os.Getenv(imageMajorEnv))
	if pinned == "" {
		if strings.TrimSpace(os.Getenv(imageRefEnv)) != "" {
			t.Fatalf("%s is set without %s: an image reference says nothing about which major it is", imageRefEnv, imageMajorEnv)
		}
		return entries
	}
	if strings.TrimSpace(os.Getenv(imageRefEnv)) == "" {
		t.Fatalf("%s is set without %s: nothing says which image to test", imageMajorEnv, imageRefEnv)
	}
	for _, entry := range entries {
		if entry.Major == pinned {
			return []config.PostgresMajorEntry{entry}
		}
	}
	t.Fatalf("%s=%s is not a major in the catalogue (%s)", imageMajorEnv, pinned, config.SupportedPostgresMajorsMessage())
	return nil
}

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

// DocumentDB refuses to load unless its libraries are preloaded, and its DDL
// path needs pg_cron. Upstream's own scripts/preload_libraries.sh produces
// exactly this list for a non-distributed build; carrying more would be
// guessing, carrying less does not start.
const documentDBPreloadLibraries = "pg_cron, pg_documentdb_core, pg_documentdb"

func startImageForMajor(ctx context.Context, t *testing.T, entry config.PostgresMajorEntry) *sql.DB {
	t.Helper()

	buildArgs := map[string]*string{
		"BASE_IMAGE": &entry.BaseImage,
		"PG_MAJOR":   &entry.Major,
	}
	documentDBRef := ""
	// Written into postgresql.conf rather than passed as pg_ctl options: the
	// library list has to be quoted, and quoting it inside the entrypoint's
	// own quoting is how this silently fails to start.
	configureDocumentDB := ""
	if entry.DocumentDB {
		documentDBRef = config.DocumentDBRef()
		configureDocumentDB = fmt.Sprintf(
			"printf \"shared_preload_libraries = '%s'\\ncron.database_name = 'postgres'\\n\" >> /tmp/pgdata/postgresql.conf && ",
			documentDBPreloadLibraries)
	}
	buildArgs["DOCUMENTDB_REF"] = &documentDBRef

	// An already-built image wins: the publish workflow points this at the
	// artefact it is about to push, so what gets verified is that image and
	// not a rebuild that merely shares a Dockerfile with it.
	prebuilt := strings.TrimSpace(os.Getenv(imageRefEnv))
	fromDockerfile := testcontainers.FromDockerfile{
		Context:       dockerfileContext(t),
		BuildArgs:     buildArgs,
		KeepImage:     true,
		PrintBuildLog: false,
	}
	if prebuilt != "" {
		fromDockerfile = testcontainers.FromDockerfile{}
	}

	container, err := postgres.Run(ctx, prebuilt,
		testcontainers.CustomizeRequest(testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				FromDockerfile: fromDockerfile,
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
						configureDocumentDB +
						// listen_addresses covers localhost too: DocumentDB's DDL
						// path opens a libpq connection back to the server.
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
	requireImageTests(t)
	introspector := &Introspector{}

	for _, entry := range majorsUnderTest(t) {
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

// A major is DocumentDB-capable only if the extension actually works there.
// Proving the files are present proves nothing — an extension can ship and
// still fail to create, load or store a document. So each capable major
// creates the extension, creates a collection, inserts a document, reads it
// back, and is asked what it has installed.
func TestDocumentDBWorksOnEveryMajorThatClaimsIt(t *testing.T) {
	requireImageTests(t)
	for _, entry := range majorsUnderTest(t) {
		if !entry.DocumentDB {
			continue
		}
		t.Run("postgres"+entry.Major, func(t *testing.T) {
			ctx := context.Background()
			db := startImageForMajor(ctx, t, entry)

			if _, err := db.ExecContext(ctx, "CREATE EXTENSION documentdb CASCADE"); err != nil {
				t.Fatalf("major %s: CREATE EXTENSION documentdb: %v", entry.Major, err)
			}
			if _, err := db.ExecContext(ctx, "SELECT documentdb_api.create_collection('smoke', 'docs')"); err != nil {
				t.Fatalf("major %s: create_collection: %v", entry.Major, err)
			}
			if _, err := db.ExecContext(ctx,
				`SELECT documentdb_api.insert_one('smoke', 'docs', '{"_id":1,"name":"excalibase"}')`); err != nil {
				t.Fatalf("major %s: insert_one: %v", entry.Major, err)
			}

			var document string
			err := db.QueryRowContext(ctx,
				"SELECT document::text FROM documentdb_api.collection('smoke', 'docs')").Scan(&document)
			if err != nil {
				t.Fatalf("major %s: read the inserted document back: %v", entry.Major, err)
			}
			// The document comes back as BSON; the field value is there in the
			// hex, which is enough to show it round-tripped rather than that a
			// row merely exists.
			if !strings.Contains(document, hex.EncodeToString([]byte("excalibase"))) {
				t.Errorf("major %s: document read back as %q, which does not carry what was inserted", entry.Major, document)
			}

			var version string
			if err := db.QueryRowContext(ctx,
				"SELECT extversion FROM pg_extension WHERE extname = 'documentdb'").Scan(&version); err != nil {
				t.Fatalf("major %s: documentdb does not report itself installed: %v", entry.Major, err)
			}
			if version == "" {
				t.Errorf("major %s: documentdb reports an empty version", entry.Major)
			}
		})
	}
}

// The other half of the claim: a major the catalogue says cannot offer
// DocumentDB must not have it in its image either, or the catalogue is lying
// to the API that refuses provisioning on its word.
func TestDocumentDBIsAbsentFromEveryMajorThatDisclaimsIt(t *testing.T) {
	requireImageTests(t)
	for _, entry := range majorsUnderTest(t) {
		if entry.DocumentDB {
			continue
		}
		t.Run("postgres"+entry.Major, func(t *testing.T) {
			ctx := context.Background()
			db := startImageForMajor(ctx, t, entry)

			var available bool
			err := db.QueryRowContext(ctx,
				"SELECT EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'documentdb')").Scan(&available)
			if err != nil {
				t.Fatalf("major %s: query pg_available_extensions: %v", entry.Major, err)
			}
			if available {
				t.Errorf("major %s: catalogue says documentdb=false, image carries it", entry.Major)
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
	requireImageTests(t)

	for _, entry := range majorsUnderTest(t) {
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
