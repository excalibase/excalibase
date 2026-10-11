package provisioner

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// DockerMySQLProvisioner runs MySQL projects as one container each on a
// single host (EXC-592): one instance, HA stays on Kubernetes (ADR 0038).
// Every lifecycle step waits on a real query over the socket, never on the
// container's state alone.
type DockerMySQLProvisioner struct {
	docker DockerClient
	// readyPoller bounds a wait for mysqld to answer; stopPoller a wait for
	// the container to stop or go.
	readyPoller Poller
	stopPoller  Poller
	// hostPorts holds each project's host port; nil lets the engine pick one
	// on every start.
	hostPorts HostPortAllocator
}

// HostPortAllocator holds one host port per project until it is deleted.
type HostPortAllocator interface {
	Allocate(ctx context.Context, projectID string) (int, error)
	Release(ctx context.Context, projectID string) error
}

// VolumeClient holds named volumes, which outlive the containers using them.
type VolumeClient interface {
	CreateVolume(ctx context.Context, name string) error
	RemoveVolume(ctx context.Context, name string) error
}

func (p *DockerMySQLProvisioner) volumes() (VolumeClient, error) {
	volumes, ok := p.docker.(VolumeClient)
	if !ok {
		return nil, errors.New("the engine client cannot hold a data volume")
	}
	return volumes, nil
}

// SetHostPorts publishes every container on its project's own host port.
func (p *DockerMySQLProvisioner) SetHostPorts(ports HostPortAllocator) { p.hostPorts = ports }

// defaultDockerMySQLReady covers an image pull and datadir initialisation on
// a slow disk.
const defaultDockerMySQLReady = 3 * time.Minute

func NewDockerMySQLProvisioner(docker DockerClient) *DockerMySQLProvisioner {
	return &DockerMySQLProvisioner{
		docker:      docker,
		readyPoller: NewPoller(time.Second, defaultDockerMySQLReady),
		stopPoller:  NewPoller(defaultDeletionPoll, defaultDeletionTimeout),
	}
}

// SetPollers overrides how long the provisioner waits for mysqld to answer
// and for the container to stop or go.
func (p *DockerMySQLProvisioner) SetPollers(ready, stop Poller) {
	p.readyPoller, p.stopPoller = ready, stop
}

func (p *DockerMySQLProvisioner) SupportedType() domain.DatabaseType { return domain.MySQL }

// Provision creates and starts the container, waits for a query and creates
// the project's database. The platform users are created at registration.
func (p *DockerMySQLProvisioner) Provision(ctx context.Context, req domain.ProvisioningRequest, tier config.TierConfig, cb StageCallback) (*ProvisioningResult, error) {
	cb(domain.StageValidating)
	// A single host's backups are the platform's, not MOCO's to validate.
	shape := req
	shape.Backup = nil
	database, image, err := validateMySQLRequest(shape)
	if err != nil {
		return nil, err
	}
	cb(domain.StageContainerCreation)
	container, err := p.StartOnVolume(ctx, req.ProjectName, image, tier)
	if err != nil {
		return nil, err
	}
	cb(domain.StageWaitingForReady)
	cb(domain.StageCredentialGeneration)
	if _, err := p.run(ctx, container, "CREATE DATABASE IF NOT EXISTS "+mysqlIdent(database)+";"); err != nil {
		return nil, fmt.Errorf("create database %s: %w", database, err)
	}
	cb(domain.StageCompleted)
	return &ProvisioningResult{
		Host:         container,
		Port:         mysqlPort,
		DatabaseName: database,
		Username:     MySQLUserAdmin,
		SSLMode:      "REQUIRED",
		Namespace:    container,
	}, nil
}

// StartOnVolume creates and starts the project's container on image over its
// data volume (initialised when empty) and returns once mysqld answers. A container that was created is named even on failure,
// so the caller can remove it.
func (p *DockerMySQLProvisioner) StartOnVolume(ctx context.Context, projectID, image string, plan config.TierConfig) (string, error) {
	spec, err := MySQLContainerSpec(projectID, image, plan)
	if err != nil {
		return "", err
	}
	if p.hostPorts != nil {
		port, err := p.hostPorts.Allocate(ctx, projectID)
		if err != nil {
			return "", fmt.Errorf("host port of %s: %w", projectID, err)
		}
		spec.Ports[strconv.Itoa(mysqlPort)] = strconv.Itoa(port)
	}
	volumes, err := p.volumes()
	if err != nil {
		return "", err
	}
	if err := volumes.CreateVolume(ctx, spec.DataVolumeName); err != nil {
		return "", err
	}
	if _, err := p.docker.CreateContainerSpec(ctx, spec); err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}
	if err := p.start(ctx, spec.Name); err != nil {
		return spec.Name, fmt.Errorf("start container: %w", err)
	}
	return spec.Name, p.AwaitServing(ctx, spec.Name)
}

// start starts the container, waiting out a host port its predecessor has
// just freed: rootless Podman's port forwarder can hold it a moment longer.
func (p *DockerMySQLProvisioner) start(ctx context.Context, container string) error {
	var last error
	err := p.readyPoller.WaitUntilReady(ctx, "host port of "+container, func(ctx context.Context) (bool, error) {
		last = p.docker.StartContainer(ctx, container)
		if last != nil && strings.Contains(last.Error(), "address already in use") {
			return false, nil
		}
		return true, last
	})
	if err != nil && last != nil {
		return last
	}
	return err
}

// AwaitServing returns once a query answers over the socket.
func (p *DockerMySQLProvisioner) AwaitServing(ctx context.Context, container string) error {
	if err := p.readyPoller.WaitUntilReady(ctx, "MySQL in "+container, func(ctx context.Context) (bool, error) {
		return p.serving(ctx, container), nil
	}); err != nil {
		return fmt.Errorf("wait for mysql to answer: %w", err)
	}
	return nil
}

// serving reports whether the container runs and mysqld answers a query.
func (p *DockerMySQLProvisioner) serving(ctx context.Context, container string) bool {
	status, err := p.docker.ContainerStatus(ctx, container)
	if err != nil || status != containerRunning {
		return false
	}
	code, err := p.docker.ExecInContainer(ctx, container, append(mysqlRootClient(), "-e", "SELECT 1"))
	return err == nil && code == 0
}

// GetStatus is the observed state: the container and a real query.
func (p *DockerMySQLProvisioner) GetStatus(ctx context.Context, namespace, _ string) (*ProvisioningStatus, error) {
	if p.serving(ctx, namespace) {
		return &ProvisioningStatus{Phase: "Running", Ready: true, Message: containerRunning}, nil
	}
	status, err := p.docker.ContainerStatus(ctx, namespace)
	if err != nil {
		return nil, err
	}
	return &ProvisioningStatus{Phase: "Pending", Message: status}, nil
}

// ConfigureBackup has nothing to do: the platform takes a single host's
// MySQL backups on the plan's interval.
func (p *DockerMySQLProvisioner) ConfigureBackup(context.Context, string, string, string, int) error {
	return nil
}

// StopReplication has nothing to stop: a single host runs no CDC watcher.
func (p *DockerMySQLProvisioner) StopReplication(context.Context, string, string) error { return nil }

// Pause stops the container, keeping its volume, and returns once it is
// observed stopped.
func (p *DockerMySQLProvisioner) Pause(ctx context.Context, namespace, _ string) error {
	if namespace == "" {
		return fmt.Errorf("docker pause: the project's container is not recorded")
	}
	if err := p.docker.StopContainer(ctx, namespace); err != nil {
		return fmt.Errorf("stop container: %w", err)
	}
	return p.stopPoller.WaitUntilClear(ctx, "running container "+namespace, func(ctx context.Context) ([]string, error) {
		return p.stillRunning(ctx, namespace)
	})
}

func (p *DockerMySQLProvisioner) stillRunning(ctx context.Context, container string) ([]string, error) {
	status, err := p.docker.ContainerStatus(ctx, container)
	if err != nil || status != containerRunning {
		return nil, err
	}
	return []string{container}, nil
}

// Resume starts the container and returns once mysqld answers.
func (p *DockerMySQLProvisioner) Resume(ctx context.Context, namespace, _ string) error {
	if namespace == "" {
		return fmt.Errorf("docker resume: the project's container is not recorded")
	}
	if err := p.start(ctx, namespace); err != nil {
		return fmt.Errorf("start container: %w", err)
	}
	return p.AwaitServing(ctx, namespace)
}

// WorkloadStopped reports whether the container is not running; one the
// engine no longer knows is stopped as far as the project is concerned.
func (p *DockerMySQLProvisioner) WorkloadStopped(ctx context.Context, namespace, _ string) (bool, error) {
	running, err := p.stillRunning(ctx, namespace)
	if err != nil {
		return false, fmt.Errorf("container status: %w", err)
	}
	return len(running) == 0, nil
}

// Deprovision removes the container with its volume and returns once it is
// observed gone. An absent container counts as removed.
func (p *DockerMySQLProvisioner) Deprovision(ctx context.Context, namespace, projectID string) error {
	container := namespace
	if container == "" {
		container = MySQLContainerName(projectID)
	}
	if err := p.RemoveContainer(ctx, container); err != nil {
		return err
	}
	if err := p.removeVolume(ctx, projectID); err != nil {
		return err
	}
	if p.hostPorts == nil {
		return nil
	}
	if err := p.hostPorts.Release(ctx, projectID); err != nil {
		return fmt.Errorf("release the host port of %s: %w", projectID, err)
	}
	return nil
}

// RemoveData removes the project's container and its data, leaving its host
// port held: an in-place restore starts from empty under the same name.
func (p *DockerMySQLProvisioner) RemoveData(ctx context.Context, projectID string) error {
	if err := p.RemoveContainer(ctx, MySQLContainerName(projectID)); err != nil {
		return err
	}
	return p.removeVolume(ctx, projectID)
}

// ReplaceImage recreates the project's container on image over the same data
// and port, and returns once mysqld answers; mysqld upgrades the data
// dictionary of a newer minor version itself.
func (p *DockerMySQLProvisioner) ReplaceImage(ctx context.Context, projectID, image string, plan config.TierConfig) error {
	if err := p.RemoveContainer(ctx, MySQLContainerName(projectID)); err != nil {
		return err
	}
	_, err := p.StartOnVolume(ctx, projectID, image, plan)
	return err
}

func (p *DockerMySQLProvisioner) removeVolume(ctx context.Context, projectID string) error {
	volumes, err := p.volumes()
	if err != nil {
		return err
	}
	return volumes.RemoveVolume(ctx, MySQLVolumeName(projectID))
}

// RemoveContainer stops and removes a container and waits until it is gone.
func (p *DockerMySQLProvisioner) RemoveContainer(ctx context.Context, container string) error {
	status, err := p.docker.ContainerStatus(ctx, container)
	if err != nil {
		return fmt.Errorf("container status: %w", err)
	}
	if status == containerNotFound {
		return nil
	}
	if err := p.docker.StopContainer(ctx, container); err != nil {
		return fmt.Errorf("stop container: %w", err)
	}
	if err := p.docker.RemoveContainer(ctx, container); err != nil {
		return fmt.Errorf("remove container: %w", err)
	}
	return p.stopPoller.WaitUntilClear(ctx, "container "+container, func(ctx context.Context) ([]string, error) {
		status, err := p.docker.ContainerStatus(ctx, container)
		if err != nil || status == containerNotFound {
			return nil, err
		}
		return []string{container + " (" + status + ")"}, nil
	})
}

// RemoveDatabase removes a database added to a project: on a single host the
// container is the database.
func (p *DockerMySQLProvisioner) RemoveDatabase(ctx context.Context, namespace, projectID string) error {
	return p.Deprovision(ctx, namespace, projectID)
}
