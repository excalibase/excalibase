package service

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const testAudDB = "aud-db"

func setupAuditTest(t *testing.T) (*AuditService, *k8s.MockClient) {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	store.Create(&domain.DatabaseInstance{
		ProjectID: testAudDB, Namespace: "org-aud-db", Status: "ACTIVE",
	})
	mock.ExecOutput["org-aud-db/aud-db-postgres-1"] = "pgaudit.log|all\npgaudit.log_level|log"
	return NewAuditService(store, mock), mock
}

func TestEnableAudit(t *testing.T) {
	svc, _ := setupAuditTest(t)
	err := svc.EnableAudit(context.Background(), testAudDB, domain.AuditConfig{Enabled: true})
	if err != nil {
		t.Fatalf("EnableAudit: %v", err)
	}
}

func TestEnableAuditNotFound(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	svc := NewAuditService(store, k8s.NewMockClient())
	err := svc.EnableAudit(context.Background(), "nope", domain.AuditConfig{})
	if err == nil {
		t.Error("expected error")
	}
}

func TestGetAuditConfig(t *testing.T) {
	svc, _ := setupAuditTest(t)
	cfg, err := svc.GetAuditConfig(context.Background(), testAudDB)
	if err != nil {
		t.Fatalf("GetAuditConfig: %v", err)
	}
	if !cfg.Enabled {
		t.Error("should be enabled")
	}
	if cfg.Settings["pgaudit.log"] != "all" {
		t.Errorf("pgaudit.log: got %s", cfg.Settings["pgaudit.log"])
	}
}

func TestGetAuditLogs(t *testing.T) {
	svc, mock := setupAuditTest(t)
	mock.ExecOutput["org-aud-db/aud-db-postgres-1"] = "AUDIT: session,1,1,READ,SELECT,,,SELECT 1"
	logs, err := svc.GetAuditLogs(context.Background(), testAudDB, 100)
	if err != nil {
		t.Fatalf("GetAuditLogs: %v", err)
	}
	if logs == "" {
		t.Error("logs should not be empty")
	}
}
