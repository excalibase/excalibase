package service

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// ADR 0041 decision 6: a MySQL project's backups run on its plan's schedule
// with a temporary credential for MOCO's prefix of it, never the platform key.
func TestMySQLProjectBacksUpWithItsOwnPrefixCredential(t *testing.T) {
	svc, store, mock := setupMySQLTest(t)
	withPlatformBackupKey(t, svc)
	minter := &recordingMinter{}
	svc.SetBackupCredentials(newTestIssuer(t, minter, newFakeObjectDeleter()))
	inst := provisionMySQL(t, svc, store, "")

	if leaked := secretsHolding(mock, platformKeySecret); len(leaked) != 0 {
		t.Fatalf("the platform key reached %v", leaked)
	}
	prefix := k8s.MOCOBackupPrefix(inst.Namespace, inst.ProjectID)
	data := projectSecret(t, mock, inst.Namespace, k8s.MySQLBackupCredentialsSecret)
	if string(data["AWS_ACCESS_KEY_ID"]) != "tmp-"+prefix {
		t.Errorf("credential minted for %q, want %q", data["AWS_ACCESS_KEY_ID"], prefix)
	}
	policy, ok := mock.CRDs[inst.Namespace+"/"+k8s.MySQLBackupPolicyName(inst.ProjectID)]
	if !ok {
		t.Fatal("no BackupPolicy")
	}
	if schedule, _, _ := unstructured.NestedString(policy.Object, "spec", "schedule"); schedule != "0 */6 * * *" {
		t.Errorf("Free schedule %q", schedule)
	}
}

func TestStandardMySQLProjectIsBackedUpHourly(t *testing.T) {
	svc, store, mock := setupMySQLTest(t)
	setOrgTier(svc, "org1", domain.Standard)
	inst := provisionMySQL(t, svc, store, "")
	if inst.BackupSchedule != "0 * * * *" {
		t.Errorf("recorded schedule %q", inst.BackupSchedule)
	}
	policy := mock.CRDs[inst.Namespace+"/"+k8s.MySQLBackupPolicyName(inst.ProjectID)]
	if schedule, _, _ := unstructured.NestedString(policy.Object, "spec", "schedule"); schedule != "0 * * * *" {
		t.Errorf("Standard schedule %q", schedule)
	}
}

// A plan change carries the schedule with it.
func TestMySQLPlanChangeMovesTheBackupSchedule(t *testing.T) {
	svc, store, mock := setupMySQLTest(t)
	inst := provisionMySQL(t, svc, store, "")
	setOrgTier(svc, "org1", domain.Standard)
	if err := svc.ApplyOrgTier(context.Background(), inst.ProjectID, domain.Standard); err != nil {
		t.Fatalf("ApplyOrgTier: %v", err)
	}
	policy := mock.CRDs[inst.Namespace+"/"+k8s.MySQLBackupPolicyName(inst.ProjectID)]
	if schedule, _, _ := unstructured.NestedString(policy.Object, "spec", "schedule"); schedule != "0 * * * *" {
		t.Errorf("schedule after the plan change %q", schedule)
	}
	updated, _ := store.FindByProjectID(inst.ProjectID)
	if updated.BackupSchedule != "0 * * * *" {
		t.Errorf("recorded schedule %q", updated.BackupSchedule)
	}
}
