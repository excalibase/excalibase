package k8s

import (
	"strings"
	"testing"
)

// The purge path deletes everything under BarmanObjectPrefix. It must be
// exactly the destinationPath/serverName location the backup CRD writes to,
// otherwise a deprovision could either miss objects or delete the wrong ones.
func TestBarmanObjectPrefixMatchesBackupCRD(t *testing.T) {
	store := buildBarmanObjectStore("proj-abc", ObjectStoreOpts{Bucket: "excalibase-backups"})

	destination := store["destinationPath"].(string)
	serverName := store["serverName"].(string)
	wantPrefix := strings.TrimPrefix(destination, "s3://excalibase-backups/") + "/" + serverName + "/"

	if got := BarmanObjectPrefix("proj-abc"); got != wantPrefix {
		t.Fatalf("BarmanObjectPrefix = %q, want %q (derived from CRD)", got, wantPrefix)
	}
	if got := BarmanObjectPrefix("proj-abc"); got != "proj-abc/cloud/" {
		t.Fatalf("BarmanObjectPrefix = %q, want proj-abc/cloud/", got)
	}
}

func TestDefaultBackupBucketMatchesBackupSpecDefault(t *testing.T) {
	spec := buildBackupSpec("proj-x", &BackupOpts{RetentionDays: 7})
	store := spec["barmanObjectStore"].(map[string]interface{})
	if got := store["destinationPath"]; got != "s3://"+DefaultBackupBucket+"/proj-x" {
		t.Fatalf("destinationPath = %v, want default bucket %q", got, DefaultBackupBucket)
	}
}
