package service

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// fakeBackupRunner emulates `pg_basebackup` streaming. Captures the
// inst it was called for so tests can assert dispatch passed the
// right project context.
type fakeBackupRunner struct {
	mu          sync.Mutex
	payload     []byte // bytes the runner writes to dst on BasebackupTo
	failBackup  error
	failRestore error
	lastBackupInst  string // ProjectID
	lastRestoreInst string
	restoreSink     bytes.Buffer
}

func (f *fakeBackupRunner) BasebackupTo(_ context.Context, inst *domain.DatabaseInstance, dst io.Writer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastBackupInst = inst.ProjectID
	if f.failBackup != nil {
		return f.failBackup
	}
	if len(f.payload) == 0 {
		f.payload = []byte("base-backup-tar-bytes")
	}
	_, err := dst.Write(f.payload)
	return err
}

func (f *fakeBackupRunner) RestoreFrom(_ context.Context, inst *domain.DatabaseInstance, src io.Reader) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastRestoreInst = inst.ProjectID
	if f.failRestore != nil {
		return f.failRestore
	}
	if _, err := io.Copy(&f.restoreSink, src); err != nil {
		return err
	}
	return nil
}

// fakeS3Uploader records uploads in memory keyed by `bucket/key`.
type fakeS3Uploader struct {
	mu      sync.Mutex
	objects map[string][]byte
	failOn  string // key prefix that should fail
	aborted []string
}

func newFakeS3Uploader() *fakeS3Uploader {
	return &fakeS3Uploader{objects: make(map[string][]byte)}
}

func (u *fakeS3Uploader) Upload(_ context.Context, bucket, key string, body io.Reader) (int64, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.failOn != "" && strings.HasPrefix(key, u.failOn) {
		u.aborted = append(u.aborted, bucket+"/"+key)
		return 0, errors.New("upload failed")
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return 0, err
	}
	u.objects[bucket+"/"+key] = data
	return int64(len(data)), nil
}

func (u *fakeS3Uploader) Download(_ context.Context, bucket, key string) (io.ReadCloser, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	data, ok := u.objects[bucket+"/"+key]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (u *fakeS3Uploader) Delete(_ context.Context, bucket, key string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.objects, bucket+"/"+key)
	return nil
}

func (u *fakeS3Uploader) List(_ context.Context, bucket, prefix string) ([]S3Object, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := []S3Object{}
	for k, v := range u.objects {
		full := strings.TrimPrefix(k, bucket+"/")
		if strings.HasPrefix(full, prefix) {
			out = append(out, S3Object{Key: full, SizeBytes: int64(len(v))})
		}
	}
	return out, nil
}

// fakeBackupRecordStore is a thread-safe in-memory BackupRecordStore.
type fakeBackupRecordStore struct {
	mu      sync.Mutex
	records []domain.BackupRecord
}

func (s *fakeBackupRecordStore) Save(_ context.Context, r *domain.BackupRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.records {
		if existing.ID == r.ID {
			s.records[i] = *r
			return nil
		}
	}
	s.records = append(s.records, *r)
	return nil
}

func (s *fakeBackupRecordStore) ListByProject(_ context.Context, projectID string) ([]domain.BackupRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []domain.BackupRecord{}
	for _, r := range s.records {
		if r.ProjectID == projectID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *fakeBackupRecordStore) UpdateStatus(_ context.Context, id, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.records {
		if s.records[i].ID == id {
			s.records[i].Status = status
			return nil
		}
	}
	return errors.New("not found")
}

func setupDockerAdapter(t *testing.T) (*DockerBackupAdapter, *storage.FileSystemStore, *fakeBackupRunner, *fakeS3Uploader, *fakeBackupRecordStore) {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	runner := &fakeBackupRunner{}
	uploader := newFakeS3Uploader()
	records := &fakeBackupRecordStore{}
	adapter := NewDockerBackupAdapter(DockerBackupAdapterConfig{
		Runner:   runner,
		Uploader: uploader,
		Records:  records,
		Bucket:   "test-backups",
	})
	store.Save(&domain.DatabaseInstance{
		ProjectID:      "dk-1",
		OrgID:          "org",
		Namespace:      "excalibase-dk-1-postgres",
		DatabaseName:   "app",
		Password:       "pw",
		DeploymentMode: domain.ModeDocker,
		Status:         "ACTIVE",
	})
	return adapter, store, runner, uploader, records
}

func TestDockerAdapter_TriggerManual_RunsBasebackup(t *testing.T) {
	adapter, store, runner, _, _ := setupDockerAdapter(t)
	inst, _ := store.FindByProjectID("dk-1")

	runner.payload = []byte("test-backup-content")

	ref, err := adapter.TriggerManual(context.Background(), inst)
	if err != nil {
		t.Fatalf("TriggerManual: %v", err)
	}
	if runner.lastBackupInst != "dk-1" {
		t.Errorf("runner not called for dk-1: got %q", runner.lastBackupInst)
	}
	if ref.ID == "" {
		t.Errorf("ref.ID is empty")
	}
	if ref.ProjectID != "dk-1" {
		t.Errorf("ref.ProjectID: %s", ref.ProjectID)
	}
	if ref.Status != "COMPLETED" {
		t.Errorf("ref.Status: got %q, want COMPLETED", ref.Status)
	}
	if ref.SizeBytes != int64(len(runner.payload)) {
		t.Errorf("ref.SizeBytes: got %d, want %d", ref.SizeBytes, len(runner.payload))
	}
}

func TestDockerAdapter_TriggerManual_UploadsToCorrectS3Prefix(t *testing.T) {
	adapter, store, _, uploader, _ := setupDockerAdapter(t)
	inst, _ := store.FindByProjectID("dk-1")

	ref, err := adapter.TriggerManual(context.Background(), inst)
	if err != nil {
		t.Fatalf("TriggerManual: %v", err)
	}
	expectedKey := "test-backups/backups/dk-1/manual/" + ref.ID + ".tar.gz"
	found := false
	for k := range uploader.objects {
		if k == expectedKey {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected upload key %q, got: %v", expectedKey, mapKeys(uploader.objects))
	}
}

func TestDockerAdapter_TriggerManual_PersistsBackupRecord(t *testing.T) {
	adapter, store, _, _, records := setupDockerAdapter(t)
	inst, _ := store.FindByProjectID("dk-1")

	ref, err := adapter.TriggerManual(context.Background(), inst)
	if err != nil {
		t.Fatalf("TriggerManual: %v", err)
	}
	got, _ := records.ListByProject(context.Background(), "dk-1")
	if len(got) != 1 {
		t.Fatalf("expected 1 record, got %d", len(got))
	}
	if got[0].ID != ref.ID {
		t.Errorf("record id: got %q, want %q", got[0].ID, ref.ID)
	}
	if got[0].Status != "COMPLETED" {
		t.Errorf("record status: got %q", got[0].Status)
	}
}

func TestDockerAdapter_TriggerManual_FailsBackupRunner_LeavesFailedRecord(t *testing.T) {
	adapter, store, runner, _, records := setupDockerAdapter(t)
	inst, _ := store.FindByProjectID("dk-1")
	runner.failBackup = errors.New("pg_basebackup boom")

	_, err := adapter.TriggerManual(context.Background(), inst)
	if err == nil {
		t.Fatal("expected error")
	}
	got, _ := records.ListByProject(context.Background(), "dk-1")
	if len(got) != 1 {
		t.Fatalf("expected 1 FAILED record, got %d", len(got))
	}
	if got[0].Status != "FAILED" {
		t.Errorf("record status: got %q, want FAILED", got[0].Status)
	}
}

func TestDockerAdapter_List_ReturnsRecordsFromStore(t *testing.T) {
	adapter, store, _, _, records := setupDockerAdapter(t)
	inst, _ := store.FindByProjectID("dk-1")

	records.Save(context.Background(), &domain.BackupRecord{ID: "r1", ProjectID: "dk-1", Status: "COMPLETED", Type: "MANUAL"})
	records.Save(context.Background(), &domain.BackupRecord{ID: "r2", ProjectID: "dk-1", Status: "IN_PROGRESS", Type: "SCHEDULED"})
	// Different project — must not appear
	records.Save(context.Background(), &domain.BackupRecord{ID: "other", ProjectID: "other-project", Status: "COMPLETED"})

	refs, err := adapter.List(context.Background(), inst)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(refs) != 2 {
		t.Errorf("expected 2 records for dk-1, got %d", len(refs))
	}
	for _, r := range refs {
		if r.ProjectID != "dk-1" {
			t.Errorf("IDOR: got record for project %q on dk-1 list", r.ProjectID)
		}
	}
}

func TestDockerAdapter_List_FiltersByProjectID_NotByRequest(t *testing.T) {
	// Even if the caller somehow passed a different project's instance,
	// the adapter must source the filter from inst.ProjectID, never from
	// any incoming request. This is the IDOR contract from §8.1.
	adapter, store, _, _, records := setupDockerAdapter(t)
	inst, _ := store.FindByProjectID("dk-1")
	records.Save(context.Background(), &domain.BackupRecord{ID: "secret", ProjectID: "victim", Status: "COMPLETED"})

	refs, _ := adapter.List(context.Background(), inst)
	for _, r := range refs {
		if r.ProjectID == "victim" {
			t.Errorf("List leaked records from another project: %+v", r)
		}
	}
}

// fakeDockerClientForAdapter is a tiny mock for the Phase 1 docker
// restore path. Records the lifecycle calls + the bytes copied in.
type fakeDockerClientForAdapter struct {
	createdName string
	createdImg  string
	createdEnv  map[string]string
	started     bool
	healthy     bool
	copyDst     string
	copyBytes   int64
	failOn      string // "create" | "copy" | "start" | "health"
}

func (f *fakeDockerClientForAdapter) CreateContainer(_ context.Context, name, img string, env map[string]string, _ map[string]string) (string, error) {
	if f.failOn == "create" {
		return "", errors.New("create failed")
	}
	f.createdName = name
	f.createdImg = img
	f.createdEnv = env
	return "container-" + name, nil
}
func (f *fakeDockerClientForAdapter) StartContainer(_ context.Context, _ string) error {
	if f.failOn == "start" {
		return errors.New("start failed")
	}
	f.started = true
	return nil
}
func (f *fakeDockerClientForAdapter) StopContainer(_ context.Context, _ string) error    { return nil }
func (f *fakeDockerClientForAdapter) RemoveContainer(_ context.Context, _ string) error  { return nil }
func (f *fakeDockerClientForAdapter) ContainerStatus(_ context.Context, _ string) (string, error) {
	return "running", nil
}
func (f *fakeDockerClientForAdapter) WaitForHealthy(_ context.Context, _ string) error {
	if f.failOn == "health" {
		return errors.New("never healthy")
	}
	f.healthy = true
	return nil
}
func (f *fakeDockerClientForAdapter) ExecInContainer(_ context.Context, _ string, _ []string) (int, error) {
	return 0, nil
}
func (f *fakeDockerClientForAdapter) CopyToContainer(_ context.Context, _ string, dst string, content io.Reader) error {
	if f.failOn == "copy" {
		return errors.New("copy failed")
	}
	f.copyDst = dst
	n, _ := io.Copy(io.Discard, content)
	f.copyBytes = n
	return nil
}

// minimalGzippedTar produces a one-entry tar.gz so tests have a real
// stream to push through CopyToContainer. The test fake just drains
// it; what matters is non-zero bytes through the pipeline.
func minimalGzippedTar(t *testing.T) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	body := []byte("PG_VERSION\n16\n")
	hdr := &tar.Header{Name: "PG_VERSION", Mode: 0644, Size: int64(len(body))}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	var gz bytes.Buffer
	gw := gzip.NewWriter(&gz)
	gw.Write(raw.Bytes())
	gw.Close()
	return gz.Bytes()
}

func TestDockerAdapter_Restore_HappyPath(t *testing.T) {
	adapter, store, _, uploader, records := setupDockerAdapter(t)
	adapter.SetInstanceStore(store)
	dc := &fakeDockerClientForAdapter{}
	adapter.SetDockerClient(dc)

	src, _ := store.FindByProjectID("dk-1")

	// Seed S3 with a base backup at the canonical Phase 1B key.
	tarGz := minimalGzippedTar(t)
	uploader.objects["test-backups/backups/dk-1/manual/backup-2026.tar.gz"] = tarGz
	records.Save(context.Background(), &domain.BackupRecord{
		ID: "backup-2026", ProjectID: "dk-1", Status: "COMPLETED", Type: "MANUAL",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})

	resp, err := adapter.Restore(context.Background(), src, domain.RestoreRequest{NewProjectID: "dk-restored"})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if resp.ProjectID != "dk-restored" {
		t.Errorf("ProjectID: got %q", resp.ProjectID)
	}
	if dc.createdName == "" {
		t.Error("CreateContainer was never called")
	}
	if dc.copyDst != "/var/lib/postgresql/data" {
		t.Errorf("CopyToContainer dst: got %q, want /var/lib/postgresql/data", dc.copyDst)
	}
	if dc.copyBytes == 0 {
		t.Error("no bytes copied to container")
	}
	if !dc.started {
		t.Error("StartContainer never called")
	}
	if !dc.healthy {
		t.Error("WaitForHealthy never called")
	}
	// New instance row persisted, source untouched.
	if got, _ := store.FindByProjectID("dk-restored"); got == nil {
		t.Error("restored instance not persisted")
	}
	if got, _ := store.FindByProjectID("dk-1"); got == nil {
		t.Error("source instance was deleted (must remain)")
	}
}

func TestDockerAdapter_Restore_NoBaseBackup_Errors(t *testing.T) {
	adapter, store, _, _, _ := setupDockerAdapter(t)
	adapter.SetDockerClient(&fakeDockerClientForAdapter{})
	src, _ := store.FindByProjectID("dk-1")

	// No BackupRecord, no S3 object → restore must error before
	// touching docker.
	_, err := adapter.Restore(context.Background(), src, domain.RestoreRequest{NewProjectID: "dk-fresh"})
	if err == nil {
		t.Fatal("expected error when no base backup exists")
	}
	if !strings.Contains(err.Error(), "backup") {
		t.Errorf("error should mention backup: %v", err)
	}
}

func TestDockerAdapter_Restore_NoDockerClient_Errors(t *testing.T) {
	adapter, store, _, _, _ := setupDockerAdapter(t)
	src, _ := store.FindByProjectID("dk-1")
	// Don't call SetDockerClient — should refuse rather than silently no-op.
	_, err := adapter.Restore(context.Background(), src, domain.RestoreRequest{NewProjectID: "dk-fresh"})
	if err == nil {
		t.Error("expected error when docker client not wired")
	}
}

func TestDockerAdapter_Restore_RejectsCollidingProjectID(t *testing.T) {
	adapter, store, _, _, _ := setupDockerAdapter(t)
	inst, _ := store.FindByProjectID("dk-1")
	store.Save(&domain.DatabaseInstance{ProjectID: "existing-target", OrgID: "org", Status: "ACTIVE"})
	adapter.SetInstanceStore(store)

	_, err := adapter.Restore(context.Background(), inst, domain.RestoreRequest{
		NewProjectID: "existing-target",
	})
	if err == nil {
		t.Fatal("expected error for colliding new project id")
	}
	if !strings.Contains(err.Error(), "exists") {
		t.Errorf("unexpected error: %v", err)
	}
}

func mapKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
