package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// fakeObjectDeleter is an in-memory object store that records every list and
// delete call so tests can assert pagination and batch sizes.
type fakeObjectDeleter struct {
	mu          sync.Mutex
	keys        []string
	lastBucket  string
	listCalls   int
	deleteCalls [][]string
	deleteErr   error
	listErr     error
}

func newFakeObjectDeleter(keys ...string) *fakeObjectDeleter {
	return &fakeObjectDeleter{keys: keys}
}

func (f *fakeObjectDeleter) ListKeys(_ context.Context, bucket, prefix, token string, maxKeys int32) ([]string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	f.lastBucket = bucket
	if f.listErr != nil {
		return nil, "", f.listErr
	}
	// Like S3, the token is a key marker: the page continues after it even
	// when the earlier keys have since been deleted.
	var page []string
	next := ""
	for _, key := range f.keys {
		if !strings.HasPrefix(key, prefix) || key <= token {
			continue
		}
		if len(page) == int(maxKeys) {
			next = page[len(page)-1]
			break
		}
		page = append(page, key)
	}
	return page, next, nil
}

func (f *fakeObjectDeleter) DeleteKeys(_ context.Context, _ string, keys []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls = append(f.deleteCalls, append([]string(nil), keys...))
	if f.deleteErr != nil {
		return f.deleteErr
	}
	remaining := f.keys[:0:0]
	doomed := map[string]bool{}
	for _, key := range keys {
		doomed[key] = true
	}
	for _, key := range f.keys {
		if !doomed[key] {
			remaining = append(remaining, key)
		}
	}
	f.keys = remaining
	return nil
}

func (f *fakeObjectDeleter) remaining() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.keys...)
}

func testBackupCreds() *domain.S3Credentials {
	return &domain.S3Credentials{AccessKeyID: "k", SecretAccessKey: "s", Bucket: "excalibase-backups"}
}

func newTestPurger(deleter *fakeObjectDeleter) *BackupPurger {
	return NewBackupPurger(StaticBackupStorage(testBackupCreds()), "backups/",
		func(_ context.Context, _ *domain.S3Credentials) (ObjectDeleter, error) { return deleter, nil })
}

func TestProjectBackupPrefix(t *testing.T) {
	cases := []struct {
		name    string
		mode    domain.DeploymentMode
		project string
		want    string
		wantErr error
	}{
		{"k8s follows barman destinationPath/serverName", domain.ModeK8s, "proj-1", "proj-1/cloud/", nil},
		{"empty mode defaults to k8s", "", "proj-1", "proj-1/cloud/", nil},
		{"docker follows the uploader key prefix", domain.ModeDocker, "proj-1", "backups/proj-1/", nil},
		{"byoc has no backups", domain.ModeBYOC, "proj-1", "", ErrNoBackupsForMode},
		{"empty project id is refused", domain.ModeK8s, "", "", ErrBackupPrefixUnsafe},
		{"blank project id is refused", domain.ModeDocker, "   ", "", ErrBackupPrefixUnsafe},
		{"path traversal is refused", domain.ModeK8s, "../other", "", ErrBackupPrefixUnsafe},
		{"slash in project id is refused", domain.ModeDocker, "a/b", "", ErrBackupPrefixUnsafe},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ProjectBackupPrefix(c.mode, c.project, "backups/")
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("err = %v, want %v", err, c.wantErr)
			}
			if got != c.want {
				t.Fatalf("prefix = %q, want %q", got, c.want)
			}
		})
	}
}

// A docker key prefix that swallows the project id (e.g. an operator
// misconfiguration yielding "" or a prefix without the id) must be refused
// rather than listing the whole bucket.
func TestValidateBackupPrefixRefusesUnsafe(t *testing.T) {
	cases := []struct {
		name, prefix, project string
	}{
		{"empty prefix", "", "proj-1"},
		{"prefix without project id", "backups/", "proj-1"},
		{"prefix with a different project", "backups/proj-2/", "proj-1"},
		{"prefix not terminated by slash", "backups/proj-1", "proj-1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := validateBackupPrefix(c.prefix, c.project); !errors.Is(err, ErrBackupPrefixUnsafe) {
				t.Fatalf("err = %v, want ErrBackupPrefixUnsafe", err)
			}
		})
	}
	if err := validateBackupPrefix("backups/proj-1/", "proj-1"); err != nil {
		t.Fatalf("safe prefix rejected: %v", err)
	}
}

func TestPurgeDeletesOnlyProjectPrefixInPages(t *testing.T) {
	var keys []string
	for i := 0; i < 2350; i++ {
		keys = append(keys, fmt.Sprintf("backups/proj-1/wals/%05d.gz", i))
	}
	keys = append(keys, "backups/proj-10/manual/keep.tar.gz", "backups/other/manual/keep.tar.gz")
	deleter := newFakeObjectDeleter(keys...)

	purger := newTestPurger(deleter)
	inst := &domain.DatabaseInstance{ProjectID: "proj-1", DeploymentMode: domain.ModeDocker}
	deleted, err := purger.Purge(context.Background(), inst)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if deleted != 2350 {
		t.Fatalf("deleted = %d, want 2350", deleted)
	}
	if len(deleter.deleteCalls) != 3 {
		t.Fatalf("delete batches = %d, want 3 (1000+1000+350)", len(deleter.deleteCalls))
	}
	for i, batch := range deleter.deleteCalls {
		if len(batch) > maxDeleteBatch {
			t.Fatalf("batch %d has %d keys, exceeds %d", i, len(batch), maxDeleteBatch)
		}
	}
	got := deleter.remaining()
	if len(got) != 2 || got[0] != "backups/proj-10/manual/keep.tar.gz" || got[1] != "backups/other/manual/keep.tar.gz" {
		t.Fatalf("unrelated objects touched: %v", got)
	}
}

func TestPurgeIsIdempotentOnEmptyPrefix(t *testing.T) {
	deleter := newFakeObjectDeleter("backups/other/x.tar.gz")
	purger := newTestPurger(deleter)
	inst := &domain.DatabaseInstance{ProjectID: "proj-1", DeploymentMode: domain.ModeDocker}
	deleted, err := purger.Purge(context.Background(), inst)
	if err != nil || deleted != 0 {
		t.Fatalf("deleted=%d err=%v, want 0/nil", deleted, err)
	}
	if len(deleter.deleteCalls) != 0 {
		t.Fatalf("no delete call expected on an empty prefix, got %d", len(deleter.deleteCalls))
	}
}

func TestPurgeK8sUsesBarmanPrefix(t *testing.T) {
	deleter := newFakeObjectDeleter("proj-1/cloud/base/b1/data.tar.gz", "proj-1/cloud/wals/0000/x.gz", "proj-1-other/cloud/keep")
	purger := newTestPurger(deleter)
	inst := &domain.DatabaseInstance{ProjectID: "proj-1", DeploymentMode: domain.ModeK8s}
	deleted, err := purger.Purge(context.Background(), inst)
	if err != nil || deleted != 2 {
		t.Fatalf("deleted=%d err=%v, want 2/nil", deleted, err)
	}
	if rem := deleter.remaining(); len(rem) != 1 || rem[0] != "proj-1-other/cloud/keep" {
		t.Fatalf("remaining = %v", rem)
	}
}

func TestPurgeRefusesUnsafeProject(t *testing.T) {
	deleter := newFakeObjectDeleter("backups/proj-1/x")
	purger := newTestPurger(deleter)
	_, err := purger.Purge(context.Background(), &domain.DatabaseInstance{ProjectID: "", DeploymentMode: domain.ModeDocker})
	if !errors.Is(err, ErrBackupPrefixUnsafe) {
		t.Fatalf("err = %v, want ErrBackupPrefixUnsafe", err)
	}
	if deleter.listCalls != 0 || len(deleter.deleteCalls) != 0 {
		t.Fatal("object store must not be touched when the prefix is unsafe")
	}
}

func TestPurgeFailsWhenStorageNotConfigured(t *testing.T) {
	deleter := newFakeObjectDeleter()
	purger := NewBackupPurger(StaticBackupStorage(nil), "backups/",
		func(_ context.Context, _ *domain.S3Credentials) (ObjectDeleter, error) { return deleter, nil })
	_, err := purger.Purge(context.Background(), &domain.DatabaseInstance{ProjectID: "proj-1", DeploymentMode: domain.ModeDocker})
	if !errors.Is(err, ErrBackupStorageNotConfigured) {
		t.Fatalf("err = %v, want ErrBackupStorageNotConfigured", err)
	}
}

func TestPurgeK8sFallsBackToDefaultBucket(t *testing.T) {
	deleter := newFakeObjectDeleter("proj-1/cloud/x")
	purger := NewBackupPurger(StaticBackupStorage(&domain.S3Credentials{AccessKeyID: "k", SecretAccessKey: "s"}), "backups/",
		func(_ context.Context, _ *domain.S3Credentials) (ObjectDeleter, error) { return deleter, nil })
	if _, err := purger.Purge(context.Background(), &domain.DatabaseInstance{ProjectID: "proj-1", DeploymentMode: domain.ModeK8s}); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if deleter.lastBucket != "postgres-backups" {
		t.Fatalf("bucket = %q, want the CRD default postgres-backups", deleter.lastBucket)
	}
}

func TestPurgeDockerWithoutBucketIsRefused(t *testing.T) {
	deleter := newFakeObjectDeleter("backups/proj-1/x")
	purger := NewBackupPurger(StaticBackupStorage(&domain.S3Credentials{AccessKeyID: "k", SecretAccessKey: "s"}), "backups/",
		func(_ context.Context, _ *domain.S3Credentials) (ObjectDeleter, error) { return deleter, nil })
	_, err := purger.Purge(context.Background(), &domain.DatabaseInstance{ProjectID: "proj-1", DeploymentMode: domain.ModeDocker})
	if !errors.Is(err, ErrBackupStorageNotConfigured) {
		t.Fatalf("err = %v, want ErrBackupStorageNotConfigured", err)
	}
}

func TestPurgeSurfacesDeleteError(t *testing.T) {
	deleter := newFakeObjectDeleter("backups/proj-1/x")
	deleter.deleteErr = errors.New("boom")
	purger := newTestPurger(deleter)
	_, err := purger.Purge(context.Background(), &domain.DatabaseInstance{ProjectID: "proj-1", DeploymentMode: domain.ModeDocker})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want delete error", err)
	}
}
