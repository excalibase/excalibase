package provisioner

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const testOrg1Proj = "org1-proj"

// TestGetStatusPodNotReady covers GetStatus when the pod exists but is not
// ready, which should return Phase "Pending" with Ready=false.
func TestGetStatusPodNotReady(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.PodReady["ns/test-postgres-1"] = false // key present, value false
	prov := NewPostgreSQLProvisioner(mock, "")

	status, err := prov.GetStatus(context.Background(), "ns", "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Ready {
		t.Error("expected Ready=false for not-ready pod")
	}
	if status.Phase != "Pending" {
		t.Errorf("expected Phase=Pending, got %s", status.Phase)
	}
}

// TestGetStatusPodError covers GetStatus when IsPodReady returns an error,
// which should return Phase "Unknown" with the error message.
func TestGetStatusPodError(t *testing.T) {
	mock := k8s.NewMockClient()
	// Do NOT add the pod key — IsPodReady returns "pod not found" error
	prov := NewPostgreSQLProvisioner(mock, "")

	status, err := prov.GetStatus(context.Background(), "ns", "missing")
	if err != nil {
		t.Fatalf("GetStatus should not propagate IsPodReady errors, got: %v", err)
	}
	if status.Phase != "Unknown" {
		t.Errorf("expected Phase=Unknown, got %s", status.Phase)
	}
	if status.Message == "" {
		t.Error("expected Message to contain error description")
	}
}

// A namespace deleted under a live database Cluster wedges the operator's
// finalizers, so a failing cluster delete stops the teardown there.
func TestDeprovisionWithCRDDeleteError(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.Namespaces[testOrg1Proj] = true
	mock.DeleteCRDError = fmt.Errorf("CRD not found")
	prov := NewPostgreSQLProvisioner(mock, "")

	err := prov.Deprovision(context.Background(), testOrg1Proj, "proj")
	if err == nil {
		t.Fatal("expected the cluster delete failure to be reported")
	}
	if !mock.Namespaces[testOrg1Proj] {
		t.Error("namespace must not be deleted while the cluster delete failed")
	}
}

// TestDeprovisionNamespaceDeleteError covers the path where DeleteNamespace
// itself returns an error, which Deprovision propagates to the caller.
func TestDeprovisionNamespaceDeleteError(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.Namespaces["org1-err-proj"] = true
	mock.DeleteNamespaceError = fmt.Errorf("namespace locked")
	prov := NewPostgreSQLProvisioner(mock, "")

	err := prov.Deprovision(context.Background(), "org1-err-proj", "err-proj")
	if err == nil {
		t.Fatal("expected error when DeleteNamespace fails, got nil")
	}
}

// TestWaitForPodReadyContextCancellation covers the ctx.Done() select branch
// inside waitForPodReady.
func TestWaitForPodReadyContextCancellation(t *testing.T) {
	mock := k8s.NewMockClient()
	// Pod is never ready — IsPodReady will always return false/error
	prov := NewPostgreSQLProvisioner(mock, "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := prov.waitForPodReady(ctx, "ns", "never-ready", 30*time.Second)
	if err == nil {
		t.Fatal("expected error after context cancellation, got nil")
	}
}

// TestWaitForPodReadyTimeout covers the deadline-exceeded path where the pod
// never becomes ready within the given timeout.
func TestWaitForPodReadyTimeout(t *testing.T) {
	mock := k8s.NewMockClient()
	// Pod is never seeded as ready — every IsPodReady call returns false/error
	prov := NewPostgreSQLProvisioner(mock, "")

	// Use a very short timeout so the test completes quickly.
	err := prov.waitForPodReady(context.Background(), "ns", "never-ready", 1*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

// TestExtractCredentialsSecretNotFound covers the context-cancellation exit
// path inside extractCredentials when the secret is never available.
// A pre-cancelled context exits on the first retry so the test is instant.
func TestExtractCredentialsSecretNotFound(t *testing.T) {
	mock := k8s.NewMockClient()
	// No secret seeded at all
	prov := NewPostgreSQLProvisioner(mock, "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so the retry select fires at once

	_, err := prov.extractCredentials(ctx, "ns", "missing-secret", "proj")
	if err == nil {
		t.Fatal("expected error when secret is never found and context is cancelled, got nil")
	}
}

// TestExtractCredentialsMissingDBName covers the fallback inside
// extractCredentials when the secret exists but has no "dbname" key.
// The DatabaseName in the result should default to "app".
func TestExtractCredentialsMissingDBName(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.Secrets["ns/proj-postgres-app"] = map[string][]byte{
		"username": []byte("myuser"),
		"password": []byte("mypass"),
		// "dbname" intentionally absent
	}
	prov := NewPostgreSQLProvisioner(mock, "")

	result, err := prov.extractCredentials(context.Background(), "ns", "proj-postgres-app", "proj")
	if err != nil {
		t.Fatalf("extractCredentials: %v", err)
	}
	if result.DatabaseName != "app" {
		t.Errorf("expected DatabaseName=app (fallback), got %s", result.DatabaseName)
	}
	if result.Username != "myuser" {
		t.Errorf("expected Username=myuser, got %s", result.Username)
	}
}

// TestProvisionNamespaceCreationFailure covers the error path when
// CreateNamespace fails during Provision.
func TestProvisionNamespaceCreationFailure(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.NamespaceError = fmt.Errorf("quota exceeded")
	prov := NewPostgreSQLProvisioner(mock, "")

	tier, _ := config.GetTierConfig(domain.Free)
	_, err := prov.Provision(context.Background(), domain.ProvisioningRequest{
		PostgresVersion: "17",
		ProjectName:     "fail-proj",
		OrgID:           "org1",
		DBType:          domain.PostgreSQL,
	}, tier, func(domain.ProvisioningStage) { /* noop: test only checks error, not stage progression */ })

	if err == nil {
		t.Fatal("expected error when namespace creation fails, got nil")
	}
}

// TestProvisionCRDDeploymentFailure covers the error path when ApplyCRD fails.
func TestProvisionCRDDeploymentFailure(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.CRDError = fmt.Errorf("CRD apply rejected")
	// Namespace must be creatable
	prov := NewPostgreSQLProvisioner(mock, "")

	tier, _ := config.GetTierConfig(domain.Free)
	_, err := prov.Provision(context.Background(), domain.ProvisioningRequest{
		PostgresVersion: "17",
		ProjectName:     "crd-fail",
		OrgID:           "org1",
		DBType:          domain.PostgreSQL,
	}, tier, func(domain.ProvisioningStage) { /* noop: test only checks error, not stage progression */ })

	if err == nil {
		t.Fatal("expected error when CRD deployment fails, got nil")
	}
}

// TestProvisionWithBackupDefaultSchedule covers the branch in Provision where
// Backup.Enabled is true but Schedule is empty, so the default "0 0 * * *" is used.
func TestProvisionWithBackupDefaultSchedule(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.SetupPostgreSQLMock("sched-test", "org1-sched-test", 1)
	prov := NewPostgreSQLProvisioner(mock, "")

	tier, _ := config.GetTierConfig(domain.Free)
	_, err := prov.Provision(context.Background(), domain.ProvisioningRequest{
		PostgresVersion: "17",
		ProjectName:     "sched-test",
		OrgID:           "org1",
		DBType:          domain.PostgreSQL,
		Backup:          &domain.BackupSettings{Enabled: true, Schedule: "", Retention: 7},
	}, tier, func(domain.ProvisioningStage) { /* noop: test only checks error, not stage progression */ })

	if err != nil {
		t.Fatalf("Provision with default schedule: %v", err)
	}
	if _, ok := mock.CRDs["org1-sched-test/sched-test-postgres-backup"]; !ok {
		t.Error("ScheduledBackup CRD should have been created with default schedule")
	}
}
