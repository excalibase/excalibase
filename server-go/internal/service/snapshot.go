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

	"github.com/excalibase/provisioning-poc/internal/docbrowser"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/security"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

type SnapshotService struct {
	store       storage.InstanceStore
	k8sClient   k8s.KubeClient
	storagePath string
	documents   DocumentDumper
}

// DocumentDumper writes a DocumentDB project's collections in mongodump's
// layout (EXC-531); the document browser's gateway connector is one.
type DocumentDumper interface {
	DumpDocuments(ctx context.Context, projectID string, sink docbrowser.DumpSink) error
}

// ErrInvalidSnapshotRequest is an export request that names no dump pg_dump
// can stream back, or asks for schema only and data only at once.
var ErrInvalidSnapshotRequest = errors.New("invalid snapshot request")

func NewSnapshotService(store storage.InstanceStore, client k8s.KubeClient, storagePath string) *SnapshotService {
	return &SnapshotService{store: store, k8sClient: client, storagePath: storagePath}
}

// SetDocumentDumper wires the document export. Without it a DocumentDB
// project's export is refused rather than written without its documents.
func (s *SnapshotService) SetDocumentDumper(dumper DocumentDumper) {
	s.documents = dumper
}

// ExportsDocuments reports whether a DocumentDB project's export can carry
// its documents.
func (s *SnapshotService) ExportsDocuments() bool {
	return s.documents != nil
}

// snapshotExtensions are the files a snapshot's dump can be, by format.
var snapshotExtensions = map[string]string{"custom": ".dump", "plain": ".sql"}

func (s *SnapshotService) ExportSnapshot(ctx context.Context, projectID string, req domain.SnapshotExportRequest) (*domain.SnapshotInfo, error) {
	dir, err := s.snapshotDir(projectID)
	if err != nil {
		return nil, err
	}
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}
	format, err := snapshotFormat(req)
	if err != nil {
		return nil, err
	}
	if inst.DatabaseName == "" {
		return nil, fmt.Errorf("project %s records no database name", projectID)
	}
	if inst.DocumentDB && s.documents == nil {
		return nil, fmt.Errorf("project %s is a DocumentDB project and this platform cannot export its documents", projectID)
	}
	dump, err := s.k8sClient.ExecInPod(ctx, inst.Namespace, projectID+"-postgres-1", "postgres", pgDumpCommand(inst.DatabaseName, format, req))
	if err != nil {
		return nil, fmt.Errorf("pg_dump: %w", err)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("mkdir snapshots: %w", err)
	}
	info := &domain.SnapshotInfo{
		ID: fmt.Sprintf("%s-%s", projectID, time.Now().Format("20060102-150405")), ProjectID: projectID,
		Format: format, CreatedAt: &domain.FlexTime{Time: time.Now()}, Documents: inst.DocumentDB,
		SchemaOnly: req.SchemaOnly, DataOnly: req.DataOnly,
	}
	if err := s.writeSnapshot(ctx, inst, dir, info, []byte(dump)); err != nil {
		return nil, err
	}
	s.saveMetadata(dir, info)
	return info, nil
}

func snapshotFormat(req domain.SnapshotExportRequest) (string, error) {
	format := req.Format
	if format == "" {
		format = "custom"
	}
	if _, ok := snapshotExtensions[format]; !ok {
		return "", fmt.Errorf("%w: format must be custom or plain", ErrInvalidSnapshotRequest)
	}
	if req.SchemaOnly && req.DataOnly {
		return "", fmt.Errorf("%w: schema only and data only cannot both be asked for", ErrInvalidSnapshotRequest)
	}
	return format, nil
}

func pgDumpCommand(database, format string, req domain.SnapshotExportRequest) []string {
	cmd := []string{"pg_dump", "-U", "postgres", "-d", database, fmt.Sprintf("--format=%s", format)}
	if req.SchemaOnly {
		cmd = append(cmd, "--schema-only")
	}
	if req.DataOnly {
		cmd = append(cmd, "--data-only")
	}
	for _, t := range req.Tables {
		cmd = append(cmd, "-t", t)
	}
	for _, t := range req.ExcludeTables {
		cmd = append(cmd, "-T", t)
	}
	return cmd
}

// writeSnapshot writes the dump, or for a DocumentDB project the tar holding
// the dump and the documents. A failure leaves no file behind.
func (s *SnapshotService) writeSnapshot(ctx context.Context, inst *domain.DatabaseInstance, dir string, info *domain.SnapshotInfo, dump []byte) error {
	dumpName := inst.DatabaseName + snapshotExtensions[info.Format]
	ext := snapshotExtensions[info.Format]
	if info.Documents {
		ext = bundleExtension
	}
	info.FilePath = filepath.Join(dir, info.ID+ext)
	var err error
	if info.Documents {
		info.Size, err = s.writeDocumentBundle(ctx, inst.ProjectID, info.FilePath, dumpName, dump)
	} else {
		info.Size, err = int64(len(dump)), os.WriteFile(info.FilePath, dump, 0644)
	}
	if err != nil {
		if removeErr := os.Remove(info.FilePath); removeErr != nil && !os.IsNotExist(removeErr) {
			log.Printf("WARN: remove partial snapshot %s: %v", info.FilePath, removeErr)
		}
		return fmt.Errorf("write snapshot %s: %w", info.ID, err)
	}
	return nil
}

func (s *SnapshotService) saveMetadata(dir string, info *domain.SnapshotInfo) {
	metaData, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		log.Printf("WARN: marshal snapshot metadata %s: %v", info.ID, err)
	} else if err := os.WriteFile(filepath.Join(dir, info.ID+".json"), metaData, 0644); err != nil {
		log.Printf("WARN: write %s: %v", filepath.Join(dir, info.ID+".json"), err)
	}
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
	for _, ext := range []string{".dump", ".sql", bundleExtension} {
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
	for _, ext := range []string{".json", ".dump", ".sql", bundleExtension} {
		if err := os.Remove(filepath.Join(dir, info.ID+ext)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete snapshot: %w", err)
		}
	}
	return nil
}
