package pgready

import (
	"context"
	"io"
	"testing"

	"github.com/testcontainers/testcontainers-go"
)

// LogDumper is the part of a container needed to read its logs.
type LogDumper interface {
	Logs(ctx context.Context) (io.ReadCloser, error)
}

var _ LogDumper = testcontainers.Container(nil)

// DumpLogsOnFailure registers a cleanup that prints the container's logs when
// the test failed, so a CI failure shows what Postgres did (TLS handshake
// error, pg_hba rejection, restart).
func DumpLogsOnFailure(t *testing.T, container LogDumper) {
	t.Helper()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		reader, err := container.Logs(context.Background())
		if err != nil {
			t.Logf("postgres container logs unavailable: %v", err)
			return
		}
		defer reader.Close()
		logs, err := io.ReadAll(reader)
		if err != nil {
			t.Logf("postgres container logs cut short: %v", err)
		}
		t.Logf("postgres container logs:\n%s", logs)
	})
}
