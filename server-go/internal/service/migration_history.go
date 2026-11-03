package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/security"
)

// MigrationHistoryCopier carries a project's migration history onto a
// restored copy. The history is kept under STORAGE_PATH, not in the
// database, so a backup does not bring it (EXC-639).
type MigrationHistoryCopier interface {
	CopyMigrationHistory(sourceID, targetID string) error
}

// ErrMigrationHistoryNotConfigured refuses a restore when the platform has no
// way to carry the source's migration history to the copy.
var ErrMigrationHistoryNotConfigured = errors.New("restore: the source's migration history cannot be carried to the copy")

// MigrationHistoryFiles is the migration history as MigrationService writes
// it: one JSON record per migration under <root>/projects/<id>/migrations.
type MigrationHistoryFiles struct{ root string }

func NewMigrationHistoryFiles(root string) *MigrationHistoryFiles {
	return &MigrationHistoryFiles{root: root}
}

func (h *MigrationHistoryFiles) dir(projectID string) string {
	return filepath.Join(h.root, "projects", projectID, "migrations")
}

// CopyMigrationHistory gives targetID the source's records, ids and order
// unchanged, owned by the copy. The target is a freshly allocated project, so
// whatever an earlier attempt left there is replaced rather than merged.
func (h *MigrationHistoryFiles) CopyMigrationHistory(sourceID, targetID string) error {
	for _, id := range []string{sourceID, targetID} {
		if _, err := security.SafePathComponent(id); err != nil {
			return fmt.Errorf("copy migration history: project id %q: %w", id, err)
		}
	}
	records, err := h.readRecords(sourceID)
	if err != nil {
		return err
	}
	staging, err := h.stage(targetID, records)
	if err != nil {
		return err
	}
	target := h.dir(targetID)
	if err := os.RemoveAll(target); err != nil {
		_ = os.RemoveAll(staging)
		return fmt.Errorf("clear migration history of %s: %w", targetID, err)
	}
	if err := os.Rename(staging, target); err != nil {
		_ = os.RemoveAll(staging)
		return fmt.Errorf("place migration history of %s: %w", targetID, err)
	}
	return nil
}

// readRecords returns the source's records keyed by file name. A record that
// cannot be parsed is not listed by ListMigrations either, so it is skipped.
func (h *MigrationHistoryFiles) readRecords(projectID string) (map[string]domain.MigrationRecord, error) {
	dir := h.dir(projectID)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read migration history of %s: %w", projectID, err)
	}
	records := make(map[string]domain.MigrationRecord, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read migration record %s/%s: %w", projectID, name, err)
		}
		var record domain.MigrationRecord
		if err := json.Unmarshal(data, &record); err != nil {
			log.Printf("WARN: migration record %s/%s is unreadable and is not copied: %v", projectID, name, err)
			continue
		}
		records[name] = record
	}
	return records, nil
}

// stage writes the copy's records into a fresh directory beside the target,
// so the target only ever holds a complete history. Only this server reads
// them, so the directory and records are its own (0700, 0600).
func (h *MigrationHistoryFiles) stage(targetID string, records map[string]domain.MigrationRecord) (string, error) {
	parent := filepath.Dir(h.dir(targetID))
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", parent, err)
	}
	staging, err := os.MkdirTemp(parent, "migrations-copy-")
	if err != nil {
		return "", fmt.Errorf("stage migration history of %s: %w", targetID, err)
	}
	for name, record := range records {
		record.ProjectID = targetID
		data, err := json.MarshalIndent(record, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(staging, name), data, 0o600)
		}
		if err != nil {
			_ = os.RemoveAll(staging)
			return "", fmt.Errorf("stage migration record %s of %s: %w", name, targetID, err)
		}
	}
	return staging, nil
}
