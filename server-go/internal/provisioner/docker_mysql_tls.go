package provisioner

import (
	"context"
	"fmt"
	"time"
)

// A single host has no cert-manager: the platform issues a MySQL project's
// CA and server certificate itself, like the DocumentDB gateway's (ADR 0039),
// valid for ten years with the CA key discarded (EXC-589). They live on the
// data volume, so a container recreated over it (upgrade, plan change) keeps
// presenting what clients already trust; a restore into a new volume issues
// new ones and the platform refiles the CA.

const (
	// MySQLTLSDir is where mysqld reads its certificate, on the data volume.
	MySQLTLSDir = MySQLDataDir + "/.excalibase-tls"
	// mysqlTLSStaging is where fresh material is written before the first
	// start; the datadir must stay empty until mysqld initialises it.
	mysqlTLSStagingParent = "/tmp"
	mysqlTLSStagingName   = "excalibase-mysql-tls"
	mysqlTLSStaging       = mysqlTLSStagingParent + "/" + mysqlTLSStagingName
)

// writeMySQLTLS writes freshly issued material into a created container; its
// start script keeps what the volume already holds.
func writeMySQLTLS(ctx context.Context, docker DockerClient, container string) error {
	material, err := newServerTLS(container, "MySQL", time.Now())
	if err != nil {
		return err
	}
	archive, err := material.tarball(mysqlTLSStagingName, MySQLImageOwner)
	if err != nil {
		return err
	}
	if err := docker.CopyToContainer(ctx, container, mysqlTLSStagingParent, archive); err != nil {
		return fmt.Errorf("write the MySQL server certificate: %w", err)
	}
	return nil
}

// mysqlTLSScript installs the staged material on a volume that has none.
func mysqlTLSScript() string {
	return `if [ ! -f ` + MySQLTLSDir + `/tls.crt ]; then
  mkdir -p ` + MySQLTLSDir + ` && cp ` + mysqlTLSStaging + `/* ` + MySQLTLSDir + `/ && chmod 600 ` + MySQLTLSDir + `/*
fi
`
}

// ServerCA is the PEM of the CA the project's server certificate is signed
// by, read from the volume the running container serves.
func (p *DockerMySQLProvisioner) ServerCA(ctx context.Context, container, _ string) (string, error) {
	return MySQLServerCA(ctx, p.docker, container)
}

// MySQLServerCA reads a MySQL container's CA.
func MySQLServerCA(ctx context.Context, docker DockerClient, container string) (string, error) {
	ca, err := readContainerFile(ctx, docker, container, MySQLTLSDir+"/"+tlsCAFile)
	if err != nil {
		return "", fmt.Errorf("read the MySQL CA of %s: %w", container, err)
	}
	return string(ca), nil
}

// ReloadTLS has nothing to do: the certificate is not renewed on a single host.
func (p *DockerMySQLProvisioner) ReloadTLS(context.Context, string, string) error { return nil }
