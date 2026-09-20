package provisioner

import (
	"context"
	"errors"
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

// StopContainer returning is not proof the database is down: the daemon
// gives postgres a shutdown grace period. The pause must observe the
// container leave the running state.
func TestDockerPauseWaitsForTheContainerToLeaveRunning(t *testing.T) {
	docker := newMockDocker()
	docker.containers["c1"] = "running"
	docker.stopKeepsRunning = true
	p := NewDockerPostgreSQLProvisioner(docker)
	p.SetPausePoller(pauseObservationPoller(func(round int) {
		if round == 2 {
			docker.containers["c1"] = "exited"
		}
	}))

	if err := p.Pause(context.Background(), "c1", "proj"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
}

func TestDockerPauseFailsWhileTheContainerIsStillRunning(t *testing.T) {
	docker := newMockDocker()
	docker.containers["c1"] = "running"
	docker.stopKeepsRunning = true
	p := NewDockerPostgreSQLProvisioner(docker)
	p.SetPausePoller(pauseObservationPoller(nil))

	if err := p.Pause(context.Background(), "c1", "proj"); !errors.Is(err, ErrWaitTimeout) {
		t.Fatalf("err: got %v, want ErrWaitTimeout", err)
	}
}

// Docker mode runs no per-project CDC watcher, so there is no replication
// session to end before the container stops.
func TestDockerStopReplicationIsANoOp(t *testing.T) {
	p := NewDockerPostgreSQLProvisioner(newMockDocker())
	if err := p.StopReplication(context.Background(), "c1", "proj"); err != nil {
		t.Fatalf("StopReplication: %v", err)
	}
}

func TestDockerPauseReportsAStopTheDaemonRefused(t *testing.T) {
	docker := newMockDocker()
	docker.containers["c1"] = "running"
	docker.failOn = "stop"
	p := NewDockerPostgreSQLProvisioner(docker)

	if err := p.Pause(context.Background(), "c1", "proj"); err == nil {
		t.Fatal("a stop the daemon refused must not be reported as a pause")
	}
}

func TestDockerWorkloadStoppedReadsTheContainerState(t *testing.T) {
	docker := newMockDocker()
	docker.containers["c1"] = "running"
	p := NewDockerPostgreSQLProvisioner(docker)

	stopped, err := p.WorkloadStopped(context.Background(), "c1", "proj")
	if err != nil || stopped {
		t.Errorf("a running container is not stopped: %v %v", stopped, err)
	}

	docker.containers["c1"] = "exited"
	stopped, err = p.WorkloadStopped(context.Background(), "c1", "proj")
	if err != nil || !stopped {
		t.Errorf("an exited container is stopped: %v %v", stopped, err)
	}

	// A container the daemon no longer knows about is stopped too.
	delete(docker.containers, "c1")
	stopped, err = p.WorkloadStopped(context.Background(), "c1", "proj")
	if err != nil || !stopped {
		t.Errorf("a missing container is stopped: %v %v", stopped, err)
	}

	if _, err := p.WorkloadStopped(context.Background(), "", "proj"); err == nil {
		t.Error("a missing container id must be reported, not guessed")
	}
}

func TestDockerPauseRefusesAMissingContainerID(t *testing.T) {
	p := NewDockerPostgreSQLProvisioner(newMockDocker())
	if err := p.Pause(context.Background(), "", "proj"); err == nil {
		t.Error("a missing container id must be reported, not guessed")
	}
}

func TestDockerWorkloadStoppedReportsADaemonItCannotAsk(t *testing.T) {
	docker := newMockDocker()
	docker.failOn = "status"
	p := NewDockerPostgreSQLProvisioner(docker)

	if _, err := p.WorkloadStopped(context.Background(), "c1", "proj"); err == nil {
		t.Error("a daemon that cannot be asked must not be reported as stopped")
	}
}
