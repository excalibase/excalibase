package main

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
)

// EXC-560: customer files have their own bucket and key; the backup store's
// R2_* settings never turn the file store on.
func TestBuildStorageService_UsesTheFileStoreOnly(t *testing.T) {
	backupsOnly := config.AppConfig{
		R2AccessKeyID: "k", R2SecretAccessKey: "s", R2Endpoint: "https://r2.example", R2Bucket: "excalibase-backups",
	}
	if buildStorageService(backupsOnly, nil) != nil {
		t.Fatal("the backup store's key turned customer file storage on")
	}
	files := config.AppConfig{FileStorage: config.FileStorageConfig{
		AccessKeyID: "k", SecretAccessKey: "s", Endpoint: "https://r2.example", Bucket: "excalibase-files", Region: "auto",
	}}
	if buildStorageService(files, nil) == nil {
		t.Fatal("a complete file store did not turn storage on")
	}
}
