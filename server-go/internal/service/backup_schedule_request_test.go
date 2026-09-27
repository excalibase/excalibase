package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const platformBackupSchedule = "0 0 4 * * *"

func provisionWithBackup(t *testing.T, svc *ProvisioningService, backup *domain.BackupSettings) (*domain.ProvisioningResponse, error) {
	t.Helper()
	setOrgTier(svc, "org", domain.Standard)
	return svc.Provision(context.Background(), domain.ProvisioningRequest{
		PostgresVersion: "17", ProjectName: "p", OrgID: "org", DBType: domain.PostgreSQL, Backup: backup,
	})
}

func withPlatformBackups(svc *ProvisioningService) {
	svc.SetBackupDefaults(&BackupDefaults{AccessKeyID: "k", SecretAccessKey: "s", Endpoint: "https://s3.example.com", Bucket: "b", Schedule: platformBackupSchedule})
}

func TestProvisionRefusesABackupScheduleCloudNativePGWouldMisread(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	withPlatformBackups(svc)

	_, err := provisionWithBackup(t, svc, &domain.BackupSettings{Enabled: true, Schedule: "0 2 * * *", Retention: 7})
	if !errors.Is(err, k8s.ErrInvalidBackupSchedule) {
		t.Fatalf("Provision = %v, want ErrInvalidBackupSchedule", err)
	}
	if all, _ := store.FindAll(); len(all) != 0 {
		t.Errorf("no project may be recorded: %v", all)
	}
	if len(mock.CRDs) != 0 || len(mock.Namespaces) != 0 {
		t.Errorf("nothing may be created: crds=%v ns=%v", mock.CRDs, mock.Namespaces)
	}
}

func TestProvisionGivesAnUnscheduledBackupThePlatformSchedule(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	withPlatformBackups(svc)

	resp, err := provisionWithBackup(t, svc, &domain.BackupSettings{Enabled: true, Retention: 7})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	scheduled := mock.CRDs[resp.Namespace+"/"+resp.ProjectID+"-postgres-backup"]
	if scheduled == nil {
		t.Fatalf("no ScheduledBackup: %v", mock.CRDs)
	}
	if schedule, _, _ := unstructured.NestedString(scheduled.Object, "spec", "schedule"); schedule != platformBackupSchedule {
		t.Errorf("schedule = %q, want the platform's %q", schedule, platformBackupSchedule)
	}
	if inst, _ := store.FindByProjectID(resp.ProjectID); inst == nil || inst.BackupSchedule != platformBackupSchedule {
		t.Errorf("the project must record the schedule it runs on: %+v", inst)
	}
}

func TestProvisionRefusesAnUnscheduledBackupWithNoPlatformSchedule(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)

	_, err := provisionWithBackup(t, svc, &domain.BackupSettings{Enabled: true, Retention: 7})
	if !errors.Is(err, k8s.ErrInvalidBackupSchedule) {
		t.Fatalf("Provision = %v, want ErrInvalidBackupSchedule", err)
	}
	if len(mock.CRDs) != 0 || len(mock.Namespaces) != 0 {
		t.Errorf("nothing may be created: crds=%v ns=%v", mock.CRDs, mock.Namespaces)
	}
}
