package provisioner

import (
	"context"
	"testing"
)

func TestDockerProvisioner_Pause(t *testing.T) {
	docker := newMockDocker()
	docker.containers["c1"] = "running"
	p := NewDockerPostgreSQLProvisioner(docker)

	if err := p.Pause(context.Background(), "c1", "proj"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if docker.containers["c1"] != "stopped" {
		t.Errorf("Pause should stop container, status=%s", docker.containers["c1"])
	}
	// Missing namespace → error.
	if err := p.Pause(context.Background(), "", "proj"); err == nil {
		t.Error("Pause with empty namespace should error")
	}
}

func TestDockerProvisioner_Resume(t *testing.T) {
	docker := newMockDocker()
	docker.containers["c1"] = "stopped"
	p := NewDockerPostgreSQLProvisioner(docker)

	if err := p.Resume(context.Background(), "c1", "proj"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if docker.containers["c1"] != "running" {
		t.Errorf("Resume should start container, status=%s", docker.containers["c1"])
	}
	// Missing namespace → error.
	if err := p.Resume(context.Background(), "", "proj"); err == nil {
		t.Error("Resume with empty namespace should error")
	}
}

func TestDockerProvisioner_Resume_HealthFailure(t *testing.T) {
	docker := newMockDocker()
	docker.containers["c1"] = "stopped"
	docker.failOn = "health"
	p := NewDockerPostgreSQLProvisioner(docker)
	if err := p.Resume(context.Background(), "c1", "proj"); err == nil {
		t.Error("Resume should fail when health check fails")
	}
}

func TestDockerProvisioner_ConfigureBackup_NoOp(t *testing.T) {
	p := NewDockerPostgreSQLProvisioner(newMockDocker())
	// Docker mode delegates backup to WAL-G sidecars; ConfigureBackup is a no-op.
	if err := p.ConfigureBackup(context.Background(), "c1", "proj", "0 2 * * *", 7); err != nil {
		t.Errorf("ConfigureBackup should be a no-op, got %v", err)
	}
}
