package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// snapshotDirFor is where the service keeps projectID's snapshots. Tests use
// it to corrupt, remove or lock the store behind the service's back.
func snapshotDirFor(storagePath, projectID string) string {
	return filepath.Join(storagePath, "snapshots", projectID)
}

// newSnapshotServiceAt builds a service over a caller-chosen storage path so
// a test can point it at an unwritable or missing directory.
func newSnapshotServiceAt(t *testing.T, storagePath string) *SnapshotService {
	t.Helper()
	svc := setupSnapshotTest(t)
	svc.storagePath = storagePath
	return svc
}

// TestSnapshotProjectIDMustBeAPathComponent pins that a project id which
// could escape the snapshots directory is refused by every entry point,
// rather than resolving to somewhere else on disk.
func TestSnapshotProjectIDMustBeAPathComponent(t *testing.T) {
	svc := setupSnapshotTest(t)
	escaping := "../../etc"

	if _, err := svc.ExportSnapshot(context.Background(), escaping, domain.SnapshotExportRequest{}); err == nil {
		t.Error("ExportSnapshot accepted an escaping project id")
	}
	if _, err := svc.ListSnapshots(escaping); err == nil {
		t.Error("ListSnapshots accepted an escaping project id")
	}
	if _, _, err := svc.DownloadSnapshot(escaping, "any"); err == nil {
		t.Error("DownloadSnapshot accepted an escaping project id")
	}
	if err := svc.DeleteSnapshot(escaping, "any"); err == nil {
		t.Error("DeleteSnapshot accepted an escaping project id")
	}
}

// TestExportSnapshotUnwritableStorage covers the directory-creation failure:
// the dump must not be reported as saved when it could not be written.
func TestExportSnapshotUnwritableStorage(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses file permission checks")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0700) })

	svc := newSnapshotServiceAt(t, filepath.Join(parent, "storage"))
	if _, err := svc.ExportSnapshot(context.Background(), testSnapDB, domain.SnapshotExportRequest{}); err == nil {
		t.Error("ExportSnapshot succeeded with an unwritable storage path")
	}
}

// TestDownloadSnapshotSucceedsForOwner exercises the read path end to end,
// including the extension probe that picks the file the export wrote.
func TestDownloadSnapshotSucceedsForOwner(t *testing.T) {
	svc := setupSnapshotTest(t)
	info, err := svc.ExportSnapshot(context.Background(), testSnapDB, domain.SnapshotExportRequest{Format: "plain"})
	if err != nil {
		t.Fatalf("ExportSnapshot: %v", err)
	}

	data, filename, err := svc.DownloadSnapshot(testSnapDB, info.ID)
	if err != nil {
		t.Fatalf("DownloadSnapshot: %v", err)
	}
	if len(data) == 0 {
		t.Error("downloaded an empty dump")
	}
	if filename != info.ID+".sql" {
		t.Errorf("filename: got %q, want %q", filename, info.ID+".sql")
	}
}

// TestDownloadSnapshotMetadataWithoutDump covers the case where the metadata
// survives but the dump itself is gone — a half-deleted snapshot must read as
// absent, not as an empty dump.
func TestDownloadSnapshotMetadataWithoutDump(t *testing.T) {
	svc := setupSnapshotTest(t)
	info, _ := svc.ExportSnapshot(context.Background(), testSnapDB, domain.SnapshotExportRequest{Format: "plain"})
	dir := snapshotDirFor(svc.storagePath, testSnapDB)
	if err := os.Remove(filepath.Join(dir, info.ID+".sql")); err != nil {
		t.Fatalf("remove dump: %v", err)
	}

	_, _, err := svc.DownloadSnapshot(testSnapDB, info.ID)
	if !errors.Is(err, ErrSnapshotNotFound) {
		t.Errorf("got %v, want ErrSnapshotNotFound", err)
	}
}

// TestFindSnapshotRejectsUnusableMetadata covers the three ways a lookup can
// fail inside the owning project's own directory. All three answer the same
// way: not found.
func TestFindSnapshotRejectsUnusableMetadata(t *testing.T) {
	dir := func(svc *SnapshotService) string { return snapshotDirFor(svc.storagePath, testSnapDB) }

	cases := []struct {
		name    string
		corrupt func(t *testing.T, svc *SnapshotService, snapshotID string)
	}{
		{
			name: "metadata is not valid JSON",
			corrupt: func(t *testing.T, svc *SnapshotService, snapshotID string) {
				path := filepath.Join(dir(svc), snapshotID+".json")
				if err := os.WriteFile(path, []byte("{not json"), 0644); err != nil {
					t.Fatalf("corrupt metadata: %v", err)
				}
			},
		},
		{
			name: "metadata names another project",
			corrupt: func(t *testing.T, svc *SnapshotService, snapshotID string) {
				path := filepath.Join(dir(svc), snapshotID+".json")
				body := []byte(`{"id":"` + snapshotID + `","projectId":"someone-else"}`)
				if err := os.WriteFile(path, body, 0644); err != nil {
					t.Fatalf("rewrite metadata: %v", err)
				}
			},
		},
		{
			name: "metadata file is gone",
			corrupt: func(t *testing.T, svc *SnapshotService, snapshotID string) {
				if err := os.Remove(filepath.Join(dir(svc), snapshotID+".json")); err != nil {
					t.Fatalf("remove metadata: %v", err)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc := setupSnapshotTest(t)
			info, err := svc.ExportSnapshot(context.Background(), testSnapDB, domain.SnapshotExportRequest{Format: "plain"})
			if err != nil {
				t.Fatalf("ExportSnapshot: %v", err)
			}
			c.corrupt(t, svc, info.ID)

			if _, _, err := svc.DownloadSnapshot(testSnapDB, info.ID); !errors.Is(err, ErrSnapshotNotFound) {
				t.Errorf("DownloadSnapshot: got %v, want ErrSnapshotNotFound", err)
			}
			if err := svc.DeleteSnapshot(testSnapDB, info.ID); !errors.Is(err, ErrSnapshotNotFound) {
				t.Errorf("DeleteSnapshot: got %v, want ErrSnapshotNotFound", err)
			}
		})
	}
}

// TestFindSnapshotRejectsUnsafeSnapshotID pins the last-line sanitizer inside
// the service, independent of the handler's own check.
func TestFindSnapshotRejectsUnsafeSnapshotID(t *testing.T) {
	svc := setupSnapshotTest(t)
	for _, id := range []string{"../escape", "", "a/b"} {
		if _, _, err := svc.DownloadSnapshot(testSnapDB, id); !errors.Is(err, ErrSnapshotNotFound) {
			t.Errorf("DownloadSnapshot(%q): got %v, want ErrSnapshotNotFound", id, err)
		}
	}
}

// TestDeleteSnapshotSurfacesRemoveFailure covers the remove-failure branch:
// a snapshot that could not actually be removed must not report success.
func TestDeleteSnapshotSurfacesRemoveFailure(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses file permission checks")
	}
	svc := setupSnapshotTest(t)
	info, err := svc.ExportSnapshot(context.Background(), testSnapDB, domain.SnapshotExportRequest{Format: "plain"})
	if err != nil {
		t.Fatalf("ExportSnapshot: %v", err)
	}

	dir := snapshotDirFor(svc.storagePath, testSnapDB)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0700) })

	err = svc.DeleteSnapshot(testSnapDB, info.ID)
	if err == nil {
		t.Fatal("DeleteSnapshot reported success on a read-only directory")
	}
	if errors.Is(err, ErrSnapshotNotFound) {
		t.Error("a removal failure must not be reported as not-found")
	}
}

// TestListSnapshotsBeforeAnyExport covers the missing-directory path: a
// project that has never exported reads as an empty list, not an error.
func TestListSnapshotsBeforeAnyExport(t *testing.T) {
	svc := setupSnapshotTest(t)
	list, err := svc.ListSnapshots(testSnapDB)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("got %d snapshots, want 0", len(list))
	}
}

// TestListSnapshotsSkipsUnusableEntries pins that a corrupt or foreign file
// in the project's own directory is skipped rather than surfaced.
func TestListSnapshotsSkipsUnusableEntries(t *testing.T) {
	svc := setupSnapshotTest(t)
	info, err := svc.ExportSnapshot(context.Background(), testSnapDB, domain.SnapshotExportRequest{Format: "plain"})
	if err != nil {
		t.Fatalf("ExportSnapshot: %v", err)
	}
	dir := snapshotDirFor(svc.storagePath, testSnapDB)
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0644); err != nil {
		t.Fatalf("write corrupt metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "foreign.json"), []byte(`{"id":"x","projectId":"other"}`), 0644); err != nil {
		t.Fatalf("write foreign metadata: %v", err)
	}

	list, err := svc.ListSnapshots(testSnapDB)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(list) != 1 || list[0].ID != info.ID {
		t.Errorf("got %+v, want only %s", list, info.ID)
	}
}
