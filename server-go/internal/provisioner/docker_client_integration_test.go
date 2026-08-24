//go:build integration

package provisioner

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

const testNonexistentContainer = "nonexistent-container-"


// Real Docker daemon lifecycle test. Requires a reachable docker socket
// (unix or DOCKER_HOST). Skipped if the daemon isn't available.
//
// Run: go test -tags=integration ./internal/provisioner/ -run TestDockerRealClient -v

func requireDocker(t *testing.T) *RealDockerClient {
	t.Helper()
	dc, err := NewRealDockerClient(DockerClientOptions{
		Host:      os.Getenv("DOCKER_HOST"),
		CertPath:  os.Getenv("DOCKER_CERT_PATH"),
		TLSVerify: os.Getenv("DOCKER_TLS_VERIFY") != "",
	})
	if err != nil {
		t.Skipf("no docker daemon available: %v", err)
	}
	return dc
}

// TestDockerRealClient_RemoveIdempotent verifies that removing a
// nonexistent container does not error — important for Deprovision to
// tolerate partial cleanup state.
func TestDockerRealClient_RemoveIdempotent(t *testing.T) {
	dc := requireDocker(t)
	defer dc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := dc.RemoveContainer(ctx, testNonexistentContainer+t.Name()); err != nil {
		t.Errorf("removing missing container should be no-op, got: %v", err)
	}
	if err := dc.StopContainer(ctx, testNonexistentContainer+t.Name()); err != nil {
		t.Errorf("stopping missing container should be no-op, got: %v", err)
	}
	status, err := dc.ContainerStatus(ctx, testNonexistentContainer+t.Name())
	if err != nil {
		t.Errorf("status of missing container should not error, got: %v", err)
	}
	if status != "not_found" {
		t.Errorf("expected not_found, got %s", status)
	}
}

// TestDockerRealClient_ProvisionPostgres spins up the full
// DockerPostgreSQLProvisioner pipeline against a real daemon and verifies
// the pipeline reaches StageCompleted with a running Postgres container.
// Cleans up on exit.
func TestDockerRealClient_ProvisionPostgres(t *testing.T) {
	dc := requireDocker(t)
	defer dc.Close()

	p := NewDockerPostgreSQLProvisioner(dc)

	projectName := fmt.Sprintf("test-pg-%d", time.Now().UnixNano()%100000)
	containerName := "excalibase-" + projectName + "-postgres"

	defer func() {
		_ = dc.StopContainer(context.Background(), containerName)
		_ = dc.RemoveContainer(context.Background(), containerName)
	}()

	var stages []domain.ProvisioningStage
	cb := func(s domain.ProvisioningStage) { stages = append(stages, s) }

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	req := domain.ProvisioningRequest{
		ProjectName: projectName,
		DBType:      domain.PostgreSQL,
	}
	result, err := p.Provision(ctx, req, config.TierConfig{}, cb)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if result.Password == "" {
		t.Error("expected generated password")
	}
	if result.DatabaseName != "app" {
		t.Errorf("expected dbname=app, got %s", result.DatabaseName)
	}

	// Verify container is actually running on the host
	status, err := dc.ContainerStatus(ctx, containerName)
	if err != nil {
		t.Fatalf("ContainerStatus: %v", err)
	}
	if status != "running" {
		t.Errorf("container should be running, got %s", status)
	}

	// Verify the pipeline reached completion
	sawCompleted := false
	for _, s := range stages {
		if s == domain.StageCompleted {
			sawCompleted = true
			break
		}
	}
	if !sawCompleted {
		t.Errorf("pipeline did not reach StageCompleted: %v", stages)
	}
}
