package service

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// Restoring a DocumentDB project on a single host (EXC-576): the base backup
// goes into a database container built the way a new DocumentDB project's is,
// and the restored project gets a gateway of its own, with a new certificate,
// before it is registered.

// officialImageOwner is the official postgres image's postgres user.
var officialImageOwner = provisioner.FileOwner{UID: 999, GID: 999}

// createRestoreContainer creates the stopped container a backup is restored
// into, and says who its postgres runs as.
func createRestoreContainer(ctx context.Context, dc provisioner.DockerClient, spec restoreContainerSpec) (string, provisioner.FileOwner, error) {
	if spec.documentDB {
		databaseLimits, _ := provisioner.DocumentDBLimits(spec.limits)
		containerSpec, err := provisioner.DocumentDBDatabaseSpec(spec.newProject, spec.major, spec.dbName, spec.newPassword, databaseLimits)
		if err != nil {
			return "", provisioner.FileOwner{}, fmt.Errorf("restore %s: %w", spec.sourceProjectID, err)
		}
		id, err := dc.CreateContainerSpec(ctx, containerSpec)
		if err != nil {
			return "", provisioner.FileOwner{}, fmt.Errorf("create restore container: %w", err)
		}
		return id, provisioner.DocumentDBImageOwner, nil
	}
	// The data directory keeps the source's users and passwords; the
	// superuser env only feeds the image's entrypoint.
	env := map[string]string{
		"POSTGRES_DB":       spec.dbName,
		"POSTGRES_USER":     defaultPostgresSuperuser,
		"POSTGRES_PASSWORD": spec.newPassword,
	}
	id, err := dc.CreateContainer(ctx, spec.containerName, spec.image, env, map[string]string{"5432": ""}, spec.limits)
	if err != nil {
		return "", provisioner.FileOwner{}, fmt.Errorf("create restore container: %w", err)
	}
	return id, officialImageOwner, nil
}

// prepareDataDir creates PGDATA in a DocumentDB container's volume, which
// holds only its parent until postgres first runs: a copy needs an existing
// destination, and postgres refuses a data directory others may read.
func prepareDataDir(ctx context.Context, dc provisioner.DockerClient, containerID string, spec restoreContainerSpec) error {
	if !spec.documentDB {
		return nil
	}
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	owner := provisioner.DocumentDBImageOwner
	header := &tar.Header{Typeflag: tar.TypeDir, Name: "data/", Mode: 0o700, Uid: owner.UID, Gid: owner.GID, ModTime: time.Now()}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	parent := provisioner.DocumentDBDataDir[:len(provisioner.DocumentDBDataDir)-len("/data")]
	if err := dc.CopyToContainer(ctx, containerID, parent, &buffer); err != nil {
		return fmt.Errorf("create the data directory: %w", err)
	}
	return nil
}

// gatewayLimits is the gateway's share of the restored project's plan.
func (s restoreContainerSpec) gatewayLimits() provisioner.ContainerLimits {
	_, gateway := provisioner.DocumentDBLimits(s.limits)
	return gateway
}

// startRestoredGateway gives a restored DocumentDB project its gateway once
// postgres has left recovery: registration then creates the document
// browser's login through it. A failure removes it with the database.
func startRestoredGateway(ctx context.Context, pc *provisioner.ProvisionContext, deps restoreDeps, inst *domain.DatabaseInstance, containerID string) error {
	if !inst.DocumentDB {
		return nil
	}
	gateway := provisioner.DocumentDBGatewayName(inst.ProjectID)
	pc.RegisterCleanup("remove restored gateway", func(ctx context.Context) error {
		return deps.dc.RemoveContainer(ctx, gateway)
	})
	if err := provisioner.StartDocumentDBGateway(ctx, deps.dc, inst.ProjectID, containerID, deps.gatewayLimits); err != nil {
		return fmt.Errorf("start the restored DocumentDB gateway: %w", err)
	}
	return nil
}
