package main

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// hostMySQLSweepInterval is how often a single host looks for MySQL projects
// due a backup; the plan's interval (1h or 6h) decides which are.
const hostMySQLSweepInterval = 5 * time.Minute

// hostMySQLBackupAdapter backs a single host's MySQL projects up into the
// platform bucket (EXC-592); nil where the host serves no MySQL.
func hostMySQLBackupAdapter(mysql *provisioner.DockerMySQLProvisioner, docker provisioner.DockerClient, streamer service.ContainerStreamer,
	uploader service.S3Uploader, records storage.BackupRecordStore, bucket string, store storage.InstanceStore) *service.HostMySQLBackupAdapter {
	if mysql == nil {
		return nil
	}
	return service.NewHostMySQLBackupAdapter(service.HostMySQLBackupAdapterConfig{
		Docker: docker, MySQL: mysql, Streamer: streamer, Uploader: uploader,
		Records: records, Bucket: bucket, KeyPrefix: dockerBackupKeyPrefix, Instances: store,
	})
}

// startHostMySQLBackups runs the single host's MySQL backup schedule. It runs
// whatever the mysql feature says: existing projects stay backed up.
func startHostMySQLBackups(adapter *service.HostMySQLBackupAdapter, store storage.InstanceStore) {
	if adapter == nil {
		return
	}
	go service.NewHostMySQLBackupSweep(adapter, store).Run(context.Background(), hostMySQLSweepInterval)
	log.Printf("Single-host MySQL backups: sweep every %s", hostMySQLSweepInterval)
}

// hostPostgresBackupAdapter backs a single host's Postgres projects up into
// bucket and restores them on the host ports they hold.
func hostPostgresBackupAdapter(records storage.BackupRecordStore, ports provisioner.HostPortAllocator, store storage.InstanceStore,
	runner service.BackupRunner, uploader service.S3Uploader, bucket string, docker provisioner.DockerClient) *service.DockerBackupAdapter {
	adapter := service.NewDockerBackupAdapter(service.DockerBackupAdapterConfig{
		Runner:    runner,
		Uploader:  uploader,
		Records:   records,
		Bucket:    bucket,
		KeyPrefix: dockerBackupKeyPrefix,
		Instances: store,
	})
	adapter.SetDockerClient(docker)
	adapter.SetHostPorts(ports)
	return adapter
}

// mustSingleHostPorts is singleHostPorts for wiring that cannot go on without.
func mustSingleHostPorts(cfg config.AppConfig, sqlStore storage.PlatformStore) *service.HostPortAllocator {
	ports, err := singleHostPorts(cfg, sqlStore)
	if err != nil {
		log.Fatalf("single-host host ports: %v", err)
	}
	return ports
}

// hostPostgresWALInterval is how often a single host ships its Postgres
// projects' closed WAL; with archive_timeout it bounds what a restore loses.
const hostPostgresWALInterval = time.Minute

// startHostPostgresWAL ships a single host's Postgres WAL as it closes
// (EXC-593); nil where the host has no Postgres backups.
func startHostPostgresWAL(adapter *service.DockerBackupAdapter, store storage.InstanceStore) {
	if adapter == nil {
		return
	}
	go service.NewHostPostgresWALSweep(adapter, store).Run(context.Background(), hostPostgresWALInterval)
	log.Printf("Single-host Postgres WAL: shipped every %s", hostPostgresWALInterval)
}

// singleHostPorts holds each single-host MySQL project's host port in the
// platform store's port allocator (EXC-592), from the window the engine proxy
// admits.
func singleHostPorts(cfg config.AppConfig, sqlStore storage.PlatformStore) (*service.HostPortAllocator, error) {
	endpoints, ok := sqlStore.(storage.DatabaseEndpointStore)
	if !ok {
		return nil, errors.New("the platform store holds no port allocator, so projects could not keep their host port")
	}
	return service.NewHostPortAllocator(endpoints, cfg.DBEndpointPorts, cfg.DBEndpointPortQuarantine), nil
}
