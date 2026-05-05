package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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

type MigrationService struct {
	store       storage.InstanceStore
	k8sClient   k8s.KubeClient
	storagePath string
}

func NewMigrationService(store storage.InstanceStore, client k8s.KubeClient, storagePath string) *MigrationService {
	return &MigrationService{store: store, k8sClient: client, storagePath: storagePath}
}

func (s *MigrationService) ApplyMigration(ctx context.Context, projectID string, req domain.MigrationRequest) (*domain.MigrationRecord, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}

	pod := projectID + "-postgres-1"
	now := time.Now()
	migID := fmt.Sprintf("mig-%s", now.Format("20060102-150405"))

	start := time.Now()
	out, err := s.k8sClient.ExecInPod(ctx, inst.Namespace, pod, "postgres",
		[]string{"psql", "-U", "postgres", "-d", "app", "-c", req.SQL})
	elapsed := time.Since(start).Milliseconds()

	// Compute checksum from SQL — display-only fingerprint, not used
	// for security. Truncated SHA-256 (chosen over MD5 to satisfy SAST
	// rules even though either would be functionally equivalent for
	// 8-char change detection).
	checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(req.SQL)))[:8]

	record := &domain.MigrationRecord{
		ID:              migID,
		ProjectID:       projectID,
		Version:         req.Version,
		Name:            req.Name,
		Description:     req.Description,
		SQL:             req.SQL,
		ExecutionTimeMs: elapsed,
		Checksum:        checksum,
	}

	ft := &domain.FlexTime{Time: now}
	if err != nil {
		record.Status = "FAILED"
		record.ErrorMessage = err.Error()
		record.Output = err.Error()
	} else {
		record.Status = "APPLIED"
		record.Output = out
		record.AppliedAt = ft
	}

	// Save to disk
	dir := filepath.Join(s.storagePath, "projects", projectID, "migrations")
	if mkErr := os.MkdirAll(dir, 0755); mkErr != nil {
		log.Printf("WARN: mkdir %s: %v", dir, mkErr)
	}
	data, marshalErr := json.MarshalIndent(record, "", "  ")
	if marshalErr != nil {
		log.Printf("WARN: marshal migration record %s: %v", migID, marshalErr)
	} else if writeErr := os.WriteFile(filepath.Join(dir, migID+".json"), data, 0644); writeErr != nil {
		log.Printf("WARN: write %s: %v", filepath.Join(dir, migID+".json"), writeErr)
	}

	return record, nil
}

func (s *MigrationService) ListMigrations(projectID string) ([]domain.MigrationRecord, error) {
	dir := filepath.Join(s.storagePath, "projects", projectID, "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []domain.MigrationRecord{}, nil
	}

	result := make([]domain.MigrationRecord, 0)
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		if _, err := security.SafePathComponent(e.Name()); err != nil {
			continue
		}
		data, _ := os.ReadFile(filepath.Join(dir, filepath.Base(e.Name())))
		var rec domain.MigrationRecord
		if json.Unmarshal(data, &rec) == nil {
			result = append(result, rec)
		}
	}
	return result, nil
}
