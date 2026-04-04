package service

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
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

	// Compute checksum from SQL
	checksum := fmt.Sprintf("%x", md5.Sum([]byte(req.SQL)))[:8]

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
		data, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		var rec domain.MigrationRecord
		if json.Unmarshal(data, &rec) == nil {
			result = append(result, rec)
		}
	}
	return result, nil
}
