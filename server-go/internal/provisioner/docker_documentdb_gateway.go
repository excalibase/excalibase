package provisioner

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
)

// The gateway container of a DocumentDB project on one host (EXC-576). It is
// stateless: what it serves lives in the database container, and what it
// presents is a certificate written into it before it starts.

const (
	documentDBGatewayHome   = "/home/documentdb/gateway"
	documentDBGatewayTLSDir = documentDBGatewayHome + "/tls"
	// DocumentDBGatewayCAPath is the CA a Mongo client verifies the gateway with.
	DocumentDBGatewayCAPath   = documentDBGatewayTLSDir + "/ca.crt"
	documentDBGatewayReadyGap = 200 * time.Millisecond
)

// documentDBGatewayReady bounds the wait for a started gateway to listen; it
// first waits for Postgres itself.
var documentDBGatewayReady = 2 * time.Minute

// documentDBGatewayOwner is the pinned gateway image's user.
var documentDBGatewayOwner = FileOwner{UID: 1000, GID: 1000}

// ErrDocumentDBGatewayNotReady is a gateway that did not start listening.
var ErrDocumentDBGatewayNotReady = errors.New("the DocumentDB gateway did not start listening")

// DocumentDBLimits splits a tier between the database and its gateway: the
// gateway takes a quarter of the memory and CPU, the database the rest, so the
// project as a whole never exceeds its tier.
func DocumentDBLimits(tier ContainerLimits) (database, gateway ContainerLimits) {
	gateway = ContainerLimits{MemoryBytes: tier.MemoryBytes / 4, NanoCPUs: tier.NanoCPUs / 4}
	database = ContainerLimits{MemoryBytes: tier.MemoryBytes - gateway.MemoryBytes, NanoCPUs: tier.NanoCPUs - gateway.NanoCPUs}
	return database, gateway
}

// StartDocumentDBGateway creates the project's gateway in the database
// container's network namespace, with a certificate for the names a client
// dials, starts it and waits until it listens. A gateway left over from an
// earlier attempt is replaced.
func StartDocumentDBGateway(ctx context.Context, docker DockerClient, projectID, databaseContainer string, limits ContainerLimits) error {
	name := DocumentDBGatewayName(projectID)
	if err := docker.RemoveContainer(ctx, name); err != nil {
		return fmt.Errorf("remove earlier gateway: %w", err)
	}
	material, err := newGatewayTLS(DatabaseContainerName(projectID), time.Now())
	if err != nil {
		return err
	}
	id, err := docker.CreateContainerSpec(ctx, ContainerSpec{
		Name:  name,
		Image: config.DocumentDBGatewayImage(),
		Cmd: []string{"--create-user", "false", "--pg-port", "5432",
			"--cert-path", documentDBGatewayTLSDir + "/tls.crt", "--key-file", documentDBGatewayTLSDir + "/tls.key"},
		Limits:     limits,
		StopSignal: documentDBStopSignal,
		NetnsOf:    databaseContainer,
	})
	if err != nil {
		return fmt.Errorf("create gateway: %w", err)
	}
	archive, err := material.tarball()
	if err != nil {
		return err
	}
	if err := docker.CopyToContainer(ctx, id, documentDBGatewayHome, archive); err != nil {
		return fmt.Errorf("write gateway certificate: %w", err)
	}
	if err := docker.StartContainer(ctx, id); err != nil {
		return fmt.Errorf("start gateway: %w", err)
	}
	return waitForGateway(ctx, docker, id)
}

// waitForGateway probes the gateway's port from inside its own namespace.
func waitForGateway(ctx context.Context, docker DockerClient, gateway string) error {
	probe := []string{"bash", "-c", "exec 3<>/dev/tcp/127.0.0.1/" + strconv.Itoa(config.DocumentDBGatewayPort)}
	deadline := time.Now().Add(documentDBGatewayReady)
	for time.Now().Before(deadline) {
		if code, err := docker.ExecInContainer(ctx, gateway, probe); err == nil && code == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(documentDBGatewayReadyGap):
		}
	}
	return fmt.Errorf("%w within %s", ErrDocumentDBGatewayNotReady, documentDBGatewayReady)
}

// RestartDocumentDBGateway restarts the gateway in the database's current
// network namespace. A database container that restarts gets a new one, and
// a gateway still holding the old serves nothing.
func RestartDocumentDBGateway(ctx context.Context, docker DockerClient, projectID string) error {
	name := DocumentDBGatewayName(projectID)
	if err := docker.StopContainer(ctx, name); err != nil {
		return err
	}
	if err := docker.StartContainer(ctx, name); err != nil {
		return err
	}
	return waitForGateway(ctx, docker, name)
}

// DocumentDBGatewayStale reports a running database whose gateway is not
// running or started before it, so holds a namespace that is gone.
func DocumentDBGatewayStale(ctx context.Context, docker DockerClient, projectID, databaseContainer string) (bool, error) {
	database, err := docker.ContainerState(ctx, databaseContainer)
	if err != nil || !database.Running {
		return false, err
	}
	gateway, err := docker.ContainerState(ctx, DocumentDBGatewayName(projectID))
	if err != nil {
		return false, err
	}
	if !gateway.Found {
		return false, fmt.Errorf("project %s has no DocumentDB gateway container", projectID)
	}
	return !gateway.Running || gateway.StartedAt.Before(database.StartedAt), nil
}

// DocumentDBGatewayCA reads the CA the project's gateway certificate is
// signed by, from the gateway itself.
func DocumentDBGatewayCA(ctx context.Context, docker DockerClient, projectID string) ([]byte, error) {
	stream, err := docker.CopyFromContainer(ctx, DocumentDBGatewayName(projectID), DocumentDBGatewayCAPath)
	if err != nil {
		return nil, fmt.Errorf("read the gateway CA of %s: %w", projectID, err)
	}
	defer stream.Close()
	reader := tar.NewReader(stream)
	if _, err := reader.Next(); err != nil {
		return nil, fmt.Errorf("read the gateway CA of %s: %w", projectID, err)
	}
	pem, err := io.ReadAll(io.LimitReader(reader, 64<<10))
	if err != nil || len(pem) == 0 {
		return nil, fmt.Errorf("the gateway CA of %s is empty or unreadable: %v", projectID, err)
	}
	return pem, nil
}

// tarball packs the gateway's tls directory, readable by its user only.
func (m gatewayTLS) tarball() (io.Reader, error) {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	owner := documentDBGatewayOwner
	if err := writer.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: "tls/", Mode: 0o700, Uid: owner.UID, Gid: owner.GID, ModTime: time.Now()}); err != nil {
		return nil, err
	}
	for _, file := range []struct {
		name string
		body []byte
	}{{"ca.crt", m.caPEM}, {"tls.crt", m.certPEM}, {"tls.key", m.keyPEM}} {
		header := &tar.Header{Name: "tls/" + file.name, Mode: 0o600, Size: int64(len(file.body)), Uid: owner.UID, Gid: owner.GID, ModTime: time.Now()}
		if err := writer.WriteHeader(header); err != nil {
			return nil, err
		}
		if _, err := writer.Write(file.body); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return &buffer, nil
}
