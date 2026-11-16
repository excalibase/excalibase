//go:build integration

package pgready

import (
	"context"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/imagepull"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// An init script keeps the temporary server (unix socket only) up for a while;
// ready must mean the real server, reachable over TCP, not that one.
func TestWait_ReadyMeansTheRealServerAcceptsTCP(t *testing.T) {
	ctx := context.Background()
	slowInit := testcontainers.ContainerFile{
		Reader:            strings.NewReader("#!/bin/sh\nsleep 10\n"),
		ContainerFilePath: "/docker-entrypoint-initdb.d/slow.sh",
		FileMode:          0o755,
	}
	if err := imagepull.EnsureLocal(ctx, "postgres:17-alpine"); err != nil {
		t.Fatal(err)
	}
	pg, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("app"), postgres.WithUsername("postgres"), postgres.WithPassword("pw"),
		testcontainers.WithFiles(slowInit), Wait())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Terminate(ctx) })

	code, output, err := pg.Exec(ctx, []string{"sh", "-c", `PGPASSWORD=pw psql -h 127.0.0.1 -U postgres -d app -tAc 'SELECT 1'`})
	if err != nil || code != 0 {
		t.Fatalf("ready was declared before TCP worked: code=%d err=%v", code, err)
	}
	_ = output
}
