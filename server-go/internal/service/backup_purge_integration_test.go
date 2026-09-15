//go:build integration

package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// TestBackupPurge_LocalStack runs the purge against a real S3 API: objects
// are uploaded under two projects' docker prefixes, one is purged with a
// page size smaller than the object count (so ListObjectsV2 continuation +
// DeleteObjects batching are exercised end to end), and the other project's
// objects must survive. A second purge is a no-op.
func TestBackupPurge_LocalStack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	const bucket = "purge-bucket"
	uploader := newLocalStackUploader(ctx, t, bucket)

	const doomed, survivor = "proj-doomed", "proj-doomed-2"
	for i := 0; i < 12; i++ {
		put(ctx, t, uploader, bucket, fmt.Sprintf("backups/%s/wals/%03d.gz", doomed, i))
	}
	put(ctx, t, uploader, bucket, "backups/"+doomed+"/manual/b1.tar.gz")
	put(ctx, t, uploader, bucket, "backups/"+survivor+"/manual/keep.tar.gz")
	put(ctx, t, uploader, bucket, doomed+"/cloud/base/keep-k8s-layout")

	creds := &domain.S3Credentials{AccessKeyID: "test", SecretAccessKey: "test", Bucket: bucket}
	purger := NewBackupPurger(StaticBackupStorage(creds), "backups/",
		func(context.Context, *domain.S3Credentials) (ObjectDeleter, error) { return uploader, nil })
	purger.SetPageSize(5)

	inst := &domain.DatabaseInstance{ProjectID: doomed, DeploymentMode: domain.ModeDocker}
	deleted, err := purger.Purge(ctx, inst)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if deleted != 13 {
		t.Fatalf("deleted = %d, want 13", deleted)
	}

	left, err := uploader.List(ctx, bucket, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var leftKeys []string
	for _, obj := range left {
		leftKeys = append(leftKeys, obj.Key)
	}
	want := []string{"backups/" + survivor + "/manual/keep.tar.gz", doomed + "/cloud/base/keep-k8s-layout"}
	if strings.Join(leftKeys, ",") != strings.Join(want, ",") {
		t.Fatalf("remaining = %v, want %v", leftKeys, want)
	}

	again, err := purger.Purge(ctx, inst)
	if err != nil || again != 0 {
		t.Fatalf("second purge: deleted=%d err=%v, want 0/nil", again, err)
	}
}

func put(ctx context.Context, t *testing.T, uploader *AWSS3Uploader, bucket, key string) {
	t.Helper()
	if _, err := uploader.Upload(ctx, bucket, key, strings.NewReader("x")); err != nil {
		t.Fatalf("upload %s: %v", key, err)
	}
}
