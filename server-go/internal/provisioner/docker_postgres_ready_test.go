package provisioner

import (
	"context"
	"slices"
	"testing"
	"time"
)

// initServerDocker answers like the official image during initdb: its
// temporary server takes socket connections but listens on no TCP port, and
// it stops before the real server starts.
type initServerDocker struct {
	*mockDockerClient
	tcpUp bool
}

func (m *initServerDocker) ExecInContainer(ctx context.Context, id string, cmd []string) (int, error) {
	m.execLog = append(m.execLog, cmd)
	if slices.Contains(cmd, "-h") && !m.tcpUp {
		return 2, nil
	}
	return 0, nil
}

// EXC-593: a probe answered by the init server is not "ready"; a setting sent
// then is lost when it stops (psql ALTER SYSTEM exited 2).
func TestWaitForPostgresReady_IgnoresTheInitServer(t *testing.T) {
	docker := &initServerDocker{mockDockerClient: newMockDocker()}
	p := NewDockerPostgreSQLProvisioner(docker)
	if err := p.waitForPostgresReady(context.Background(), "c1", "app", 700*time.Millisecond); err == nil {
		t.Fatal("ready while only the init server answered on the socket")
	}
	docker.tcpUp = true
	if err := p.waitForPostgresReady(context.Background(), "c1", "app", time.Second); err != nil {
		t.Fatalf("not ready once the real server listens: %v", err)
	}
}
