//go:build integration

package sites_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	pgmod "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
	"github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

// One migrated platform database serves every test in the package; each test uses its own ids.
var (
	sharedPlatformOnce sync.Once
	sharedPlatform     *postgres.Store
	sharedPlatformErr  error
)

func platformDB(t *testing.T) *postgres.Store {
	t.Helper()
	sharedPlatformOnce.Do(func() { sharedPlatform, sharedPlatformErr = startPlatformDB() })
	if sharedPlatformErr != nil {
		t.Fatalf("start platform database: %v", sharedPlatformErr)
	}
	return sharedPlatform
}

func startPlatformDB() (*postgres.Store, error) {
	ctx := context.Background()
	password := testutil.FixturePassword("sites-itest")
	container, err := pgmod.Run(ctx, "postgres:16-alpine",
		pgmod.WithDatabase("platform_test"),
		pgmod.WithUsername("platform"),
		pgmod.WithPassword(password),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		return nil, err
	}
	host, err := container.Host(ctx)
	if err != nil {
		return nil, err
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		return nil, err
	}
	dsn := url.URL{Scheme: "postgres", User: url.UserPassword("platform", password),
		Host: fmt.Sprintf("%s:%s", host, port.Port()), Path: "platform_test", RawQuery: "sslmode=disable"}
	return postgres.New(dsn.String())
}

// acceptingPublisher is a CDN that already holds every file.
type acceptingPublisher struct{}

func (acceptingPublisher) StartUploadSession(context.Context, string, map[string]cfworkers.ManifestEntry) (cfworkers.UploadSession, error) {
	return cfworkers.UploadSession{JWT: "completion"}, nil
}

func (acceptingPublisher) UploadBucket(context.Context, string, []cfworkers.Asset) (string, error) {
	return "completion", nil
}

func (acceptingPublisher) PutScript(context.Context, string, string, cfworkers.AssetConfig) error {
	return nil
}

func (acceptingPublisher) DeleteScript(context.Context, string) error { return nil }

func sampleArchive(t *testing.T) []byte {
	t.Helper()
	var raw bytes.Buffer
	zipped := gzip.NewWriter(&raw)
	archive := tar.NewWriter(zipped)
	for name, body := range map[string]string{"./index.html": "<div id=app></div>", "./assets/app.js": "1"} {
		if err := archive.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatalf("header: %v", err)
		}
		if _, err := archive.Write([]byte(body)); err != nil {
			t.Fatalf("body: %v", err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("tar: %v", err)
	}
	if err := zipped.Close(); err != nil {
		t.Fatalf("gzip: %v", err)
	}
	return raw.Bytes()
}
