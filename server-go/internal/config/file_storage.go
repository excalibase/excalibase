package config

import (
	"errors"
	"os"
	"strings"
)

// FileStorageConfig is the object store customer files live in (EXC-560): a
// bucket and key of their own, never the backup store's. Unset turns the
// storage feature off.
type FileStorageConfig struct {
	AccessKeyID     string
	SecretAccessKey string
	Endpoint        string
	Bucket          string
	Region          string
	// InternalEndpoint (STORAGE_INTERNAL_ENDPOINT): where provisioning itself
	// reaches the store when Endpoint is an address only browsers reach.
	InternalEndpoint string
}

func loadFileStorage() FileStorageConfig {
	return FileStorageConfig{
		AccessKeyID:      os.Getenv("STORAGE_ACCESS_KEY_ID"),
		SecretAccessKey:  os.Getenv("STORAGE_SECRET_ACCESS_KEY"),
		Endpoint:         os.Getenv("STORAGE_ENDPOINT"),
		Bucket:           os.Getenv("STORAGE_BUCKET"),
		Region:           os.Getenv("STORAGE_REGION"),
		InternalEndpoint: os.Getenv("STORAGE_INTERNAL_ENDPOINT"),
	}
}

// Configured reports whether any file-store setting is given.
func (f FileStorageConfig) Configured() bool {
	return f.AccessKeyID != "" || f.SecretAccessKey != "" || f.Endpoint != "" || f.Bucket != ""
}

func (f FileStorageConfig) complete() bool {
	return f.AccessKeyID != "" && f.SecretAccessKey != "" && f.Endpoint != "" && f.Bucket != ""
}

// validateFileStorage: all or nothing, and never the bucket backups go to,
// whose key and lifecycle are the backups'.
func (c AppConfig) validateFileStorage() error {
	files := c.FileStorage
	if !files.Configured() {
		return nil
	}
	if !files.complete() {
		return errors.New("file storage needs STORAGE_ACCESS_KEY_ID, STORAGE_SECRET_ACCESS_KEY, STORAGE_ENDPOINT and STORAGE_BUCKET together")
	}
	sameStore := strings.TrimRight(files.Endpoint, "/") == strings.TrimRight(c.BackupEndpoint, "/")
	if sameStore && files.Bucket == c.BackupBucket {
		return errors.New("STORAGE_BUCKET must not be the bucket backups go to: customer files need their own bucket and key")
	}
	return nil
}
