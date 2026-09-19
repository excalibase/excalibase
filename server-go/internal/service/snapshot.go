package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/security"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

type SnapshotService struct {
	store       storage.InstanceStore
	k8sClient   k8s.KubeClient
	storagePath string
}

func NewSnapshotService(store storage.InstanceStore, client k8s.KubeClient, storagePath string) *SnapshotService {
	return &SnapshotService{store: store, k8sClient: client, storagePath: storagePath}
}

func (s *SnapshotService) ExportSnapshot(ctx context.Context, projectID string, req domain.SnapshotExportRequest) (*domain.SnapshotInfo, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}

	format := "custom"
	if req.Format != "" {
		format = req.Format
	}

	snapshotID := fmt.Sprintf("%s-%s", projectID, time.Now().Format("20060102-150405"))
	ext := ".dump"
	if format == "plain" {
		ext = ".sql"
	}

	pod := projectID + "-postgres-1"
	cmd := []string{"pg_dump", "-U", "postgres", "-d", "app", fmt.Sprintf("--format=%s", format)}
	if req.SchemaOnly {
		cmd = append(cmd, "--schema-only")
	}
	for _, t := range req.Tables {
		cmd = append(cmd, "-t", t)
	}

	out, err := s.k8sClient.ExecInPod(ctx, inst.Namespace, pod, "postgres", cmd)
	if err != nil {
		return nil, fmt.Errorf("pg_dump: %w", err)
	}

	// Save to disk
	dir, err := s.snapshotDir(projectID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("mkdir snapshots: %w", err)
	}
	filePath := filepath.Join(dir, snapshotID+ext)
	if err := os.WriteFile(filePath, []byte(out), 0644); err != nil {
		return nil, fmt.Errorf("write snapshot %s: %w", filePath, err)
	}

	now := &domain.FlexTime{Time: time.Now()}
	info := &domain.SnapshotInfo{
		ID:        snapshotID,
		ProjectID: projectID,
		Format:    format,
		Size:      int64(len(out)),
		CreatedAt: now,
		FilePath:  filePath,
	}

	// Save metadata
	metaData, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		log.Printf("WARN: marshal snapshot metadata %s: %v", snapshotID, err)
	} else if err := os.WriteFile(filepath.Join(dir, snapshotID+".json"), metaData, 0644); err != nil {
		log.Printf("WARN: write %s: %v", filepath.Join(dir, snapshotID+".json"), err)
	}

	return info, nil
}

// ErrSnapshotNotFound is returned for a snapshot the project does not own as
// well as for one that does not exist. The two are deliberately the same
// answer: telling a caller that another project's snapshot id exists is
// already a disclosure (EXC-397).
var ErrSnapshotNotFound = errors.New("snapshot not found")

// snapshotDir is where projectID's snapshots live. Keying storage by project
// is the primary ownership boundary — an id from another project resolves
// into a directory that does not contain it.
func (s *SnapshotService) snapshotDir(projectID string) (string, error) {
	component, err := security.SafePathComponent(projectID)
	if err != nil {
		return "", fmt.Errorf("invalid project id")
	}
	return filepath.Join(s.storagePath, "snapshots", component), nil
}

// findSnapshot resolves a snapshot within its owning project. The directory
// already scopes the lookup; the ProjectID comparison is a second, explicit
// check so a metadata file that ever lands in the wrong directory is still
// refused rather than served.
func (s *SnapshotService) findSnapshot(projectID, snapshotID string) (*domain.SnapshotInfo, string, error) {
	dir, err := s.snapshotDir(projectID)
	if err != nil {
		return nil, "", err
	}
	id, err := security.SafePathComponent(snapshotID)
	if err != nil {
		return nil, "", ErrSnapshotNotFound
	}
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		return nil, "", ErrSnapshotNotFound
	}
	var info domain.SnapshotInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, "", ErrSnapshotNotFound
	}
	if info.ProjectID != projectID {
		return nil, "", ErrSnapshotNotFound
	}
	return &info, dir, nil
}

func (s *SnapshotService) ListSnapshots(projectID string) ([]domain.SnapshotInfo, error) {
	dir, dirErr := s.snapshotDir(projectID)
	if dirErr != nil {
		return nil, dirErr
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []domain.SnapshotInfo{}, nil
	}

	result := make([]domain.SnapshotInfo, 0)
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		if _, err := security.SafePathComponent(e.Name()); err != nil {
			continue
		}
		data, _ := os.ReadFile(filepath.Join(dir, filepath.Base(e.Name())))
		var info domain.SnapshotInfo
		if json.Unmarshal(data, &info) == nil && info.ProjectID == projectID {
			result = append(result, info)
		}
	}
	return result, nil
}

func (s *SnapshotService) DownloadSnapshot(projectID, snapshotID string) ([]byte, string, error) {
	info, dir, err := s.findSnapshot(projectID, snapshotID)
	if err != nil {
		return nil, "", err
	}
	// Try .dump then .sql
	for _, ext := range []string{".dump", ".sql"} {
		data, err := os.ReadFile(filepath.Join(dir, info.ID+ext))
		if err == nil {
			return data, info.ID + ext, nil
		}
	}
	return nil, "", ErrSnapshotNotFound
}

func (s *SnapshotService) DeleteSnapshot(projectID, snapshotID string) error {
	info, dir, err := s.findSnapshot(projectID, snapshotID)
	if err != nil {
		return err
	}
	for _, ext := range []string{".json", ".dump", ".sql"} {
		if err := os.Remove(filepath.Join(dir, info.ID+ext)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete snapshot: %w", err)
		}
	}
	return nil
}
