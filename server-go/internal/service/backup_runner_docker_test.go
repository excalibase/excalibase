package service

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// A manual backup is asked for now: a spread checkpoint makes pg_basebackup
// wait out checkpoint_completion_target first, minutes on a DocumentDB
// database whose background worker keeps pages dirty.
func TestDockerRunnerTakesTheBaseBackupWithAFastCheckpoint(t *testing.T) {
	fake := &fakeDockerSDK{stdout: []byte("tar")}
	runner := &DockerBackupRunner{sdk: fake}
	var dst bytes.Buffer
	if err := runner.BasebackupTo(context.Background(), &domain.DatabaseInstance{ProjectID: "p1", Namespace: "c1"}, &dst); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(fake.cmdSeen, "--checkpoint=fast") || fake.cmdSeen[0] != "pg_basebackup" {
		t.Fatalf("cmd %v", fake.cmdSeen)
	}
}
