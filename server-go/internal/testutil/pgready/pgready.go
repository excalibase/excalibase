// Package pgready is the one readiness check every Postgres test container
// shares. The image logs "ready to accept connections" twice (init server,
// then the real one) and a slow runner can miss the second line by seconds;
// asking the server a real query is both stricter and independent of log
// timing.
package pgready

import (
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// StartupTimeout is generous on purpose: a loaded CI runner restarts the
// server several times slower than a laptop.
const StartupTimeout = 3 * time.Minute

// Strategy waits until Postgres listens on TCP (the init-phase server only
// listens on its unix socket) and then answers SELECT 1 over TCP, with the
// password, as the container's own POSTGRES_USER and POSTGRES_DB, which the
// postgres image and module set. Going over TCP means the real server is up,
// not the init-phase one on the socket.
func Strategy() wait.Strategy {
	return wait.ForAll(
		wait.ForListeningPort("5432/tcp"),
		wait.ForExec([]string{"sh", "-c", `PGPASSWORD="$POSTGRES_PASSWORD" psql -h 127.0.0.1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc 'SELECT 1' | grep -qx 1`}),
	).WithStartupTimeoutDefault(StartupTimeout).WithDeadline(StartupTimeout)
}

// Wait is the container option to use: testcontainers.WithWaitStrategy would
// wrap Strategy in its own 60s deadline and cancel it early.
func Wait() testcontainers.CustomizeRequestOption {
	return testcontainers.WithWaitStrategyAndDeadline(StartupTimeout, Strategy())
}
