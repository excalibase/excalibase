package config

import (
	"strings"
	"testing"
)

func setFileStorageEnv(t *testing.T) {
	t.Helper()
	t.Setenv("STORAGE_ACCESS_KEY_ID", "files-key")
	t.Setenv("STORAGE_SECRET_ACCESS_KEY", "files-secret")
	t.Setenv("STORAGE_ENDPOINT", "https://acct.r2.cloudflarestorage.com")
	t.Setenv("STORAGE_BUCKET", "excalibase-files")
	t.Setenv("STORAGE_REGION", "auto")
}

func TestFileStorage_ReadsItsOwnSettings(t *testing.T) {
	setFileStorageEnv(t)
	t.Setenv("R2_BUCKET", "excalibase-backups")
	cfg := Load()
	got := cfg.FileStorage
	if got.AccessKeyID != "files-key" || got.SecretAccessKey != "files-secret" ||
		got.Endpoint != "https://acct.r2.cloudflarestorage.com" || got.Bucket != "excalibase-files" || got.Region != "auto" {
		t.Fatalf("file storage settings: %+v", got)
	}
}

func TestFileStorage_NeverFallsBackToTheBackupKey(t *testing.T) {
	t.Setenv("R2_ACCESS_KEY_ID", "backup-key")
	t.Setenv("R2_SECRET_ACCESS_KEY", "backup-secret")
	t.Setenv("R2_ENDPOINT", "https://acct.r2.cloudflarestorage.com")
	t.Setenv("R2_BUCKET", "excalibase-backups")
	cfg := Load()
	if cfg.FileStorage.Configured() {
		t.Fatalf("files took the backup store's settings: %+v", cfg.FileStorage)
	}
}

func TestValidateFileStorage(t *testing.T) {
	complete := FileStorageConfig{AccessKeyID: "k", SecretAccessKey: "s", Endpoint: "https://r2", Bucket: "files"}
	cases := []struct {
		name    string
		cfg     AppConfig
		wantErr string
	}{
		{"unset is off, not an error", AppConfig{}, ""},
		{"complete and apart from backups", AppConfig{FileStorage: complete, BackupEndpoint: "https://r2", BackupBucket: "excalibase-backups"}, ""},
		{"partial", AppConfig{FileStorage: FileStorageConfig{Bucket: "files"}}, "STORAGE_ACCESS_KEY_ID"},
		{"the backup bucket", AppConfig{FileStorage: complete, BackupEndpoint: "https://r2", BackupBucket: "files"}, "backups"},
		{"same bucket name on another store is fine", AppConfig{FileStorage: complete, BackupEndpoint: "https://minio", BackupBucket: "files"}, ""},
	}
	for _, tc := range cases {
		err := tc.cfg.validateFileStorage()
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error %v", tc.name, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s: want error naming %q, got %v", tc.name, tc.wantErr, err)
		}
	}
}

func TestValidate_RunsTheFileStorageCheck(t *testing.T) {
	cfg := AppConfig{FileStorage: FileStorageConfig{Bucket: "files"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted a partial file store")
	}
}

func TestFileStorage_InternalEndpointIsOptional(t *testing.T) {
	setFileStorageEnv(t)
	if got := Load().FileStorage.InternalEndpoint; got != "" {
		t.Fatalf("InternalEndpoint = %q with nothing set, want empty", got)
	}
	t.Setenv("STORAGE_INTERNAL_ENDPOINT", "http://objectstore:9000")
	if got := Load().FileStorage.InternalEndpoint; got != "http://objectstore:9000" {
		t.Fatalf("InternalEndpoint = %q, want the STORAGE_INTERNAL_ENDPOINT value", got)
	}
}
