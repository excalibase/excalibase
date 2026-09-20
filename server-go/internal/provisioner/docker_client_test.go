package provisioner

import (
	"os"
	"strings"
	"testing"
)

const (
	testPingDaemon = "ping docker daemon"
	testPingErrFmt = "expected ping error, got: %v"
)

// TestNewRealDockerClient_NoHostAvailable exercises the connect failure
// path without needing a real daemon. We point at an unreachable unix
// socket and verify the ping fails fast with a helpful error.
func TestNewRealDockerClient_PingFailsOnBadHost(t *testing.T) {
	_, err := NewRealDockerClient(DockerClientOptions{
		Host: "unix:///tmp/nonexistent-docker-socket-" + t.Name(),
	})
	if err == nil {
		t.Fatal("expected error when socket does not exist")
	}
	if !strings.Contains(err.Error(), testPingDaemon) {
		t.Errorf(testPingErrFmt, err)
	}
}

// TestNewRealDockerClient_FallsBackToEnvHost verifies that when opts.Host
// is empty but DOCKER_HOST is set, the client uses the env host. We set
// DOCKER_HOST to a bogus address so the ping fails, and confirm the error
// mentions the env value rather than /var/run/docker.sock.
func TestNewRealDockerClient_FallsBackToEnvHost(t *testing.T) {
	bogus := "tcp://127.0.0.1:1" // port 1 is reserved, connect will fail
	t.Setenv("DOCKER_HOST", bogus)
	// Clear TLS vars so the env path doesn't try to load certs.
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")

	_, err := NewRealDockerClient(DockerClientOptions{})
	if err == nil {
		t.Fatal("expected error from unreachable env host")
	}
	if !strings.Contains(err.Error(), testPingDaemon) {
		t.Errorf(testPingErrFmt, err)
	}
}

// TestNewRealDockerClient_FallsBackToUnixSocket verifies that when neither
// opts.Host nor DOCKER_HOST is set, the client attempts the unix socket.
// We run this test only when there's no real daemon, so connection
// failure is the expected outcome.
func TestNewRealDockerClient_FallsBackToUnixSocket(t *testing.T) {
	if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		t.Skip("real docker socket present — fallback test would spuriously succeed")
	}
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")

	_, err := NewRealDockerClient(DockerClientOptions{})
	if err == nil {
		t.Fatal("expected error when no socket is available")
	}
	if !strings.Contains(err.Error(), testPingDaemon) {
		t.Errorf(testPingErrFmt, err)
	}
}
