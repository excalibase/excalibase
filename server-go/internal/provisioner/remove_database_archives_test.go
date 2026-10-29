package provisioner

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// EXC-668: a database removed from its project takes the stores of every
// archive a major upgrade gave it, and only its own.
func TestRemoveDatabaseDeletesEveryArchiveStoreOfTheProject(t *testing.T) {
	mock := k8s.NewMockClient()
	for _, name := range []string{"proj-backups", "proj-backups-pg17", "proj-backups-pg18", "other-backups-pg17"} {
		store, err := k8s.BuildBackupObjectStore("proj", "org1-proj", k8s.ObjectStoreOpts{Bucket: "b", SecretName: k8s.BackupCredentialsSecretName}, 7)
		if err != nil {
			t.Fatal(err)
		}
		store.SetName(name)
		mock.CRDs["org1-proj/"+name] = store
	}
	prov := NewPostgreSQLProvisioner(mock, "")
	prov.SetDeletionPoller(fastPoller(3))
	if err := prov.RemoveDatabase(context.Background(), "org1-proj", "proj"); err != nil {
		t.Fatalf("RemoveDatabase: %v", err)
	}
	for name, kept := range map[string]bool{"proj-backups": false, "proj-backups-pg17": false, "proj-backups-pg18": false, "other-backups-pg17": true} {
		if _, ok := mock.CRDs["org1-proj/"+name]; ok != kept {
			t.Errorf("%s kept=%v, want %v", name, ok, kept)
		}
	}
}
