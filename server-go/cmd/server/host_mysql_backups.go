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
