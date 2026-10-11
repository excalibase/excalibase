package service

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// A MySQL project renews into its AWS-variable Secret, for MOCO's prefix of
// it; the CNPG Secret is not its.
func TestRenewalRenewsAMySQLProjectForItsMOCOPrefix(t *testing.T) {
	lab := newRenewalLab(t)
	enabled := true
	if err := lab.store.Create(&domain.DatabaseInstance{
		ProjectID: "my", OrgID: "org", Namespace: "org-my", Status: "ACTIVE",
		DBType: domain.MySQL, DeploymentMode: domain.ModeK8s, BackupEnabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	data, err := k8s.MySQLBackupAccessNameData(&domain.S3Credentials{
		AccessKeyID: "old", SecretAccessKey: "old", SessionToken: "old", ExpiresAt: lab.now.Add(time.Hour),
		IssuedBy: k8s.BackupKeyFingerprint(platformStore()), Bucket: platformStore().Bucket, Endpoint: platformStore().Endpoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	lab.mock.Secrets["org-my/"+k8s.MySQLBackupAccessName] = data

	report := lab.renewer.RenewDue(context.Background())
	if len(report.Failed) != 0 || report.Renewed != 1 {
		t.Fatalf("report %+v", report)
	}
	prefix := k8s.MOCOBackupPrefix("org-my", "my")
	renewed := lab.mock.Secrets["org-my/"+k8s.MySQLBackupAccessName]
	if string(renewed["AWS_SESSION_TOKEN"]) != "token-"+prefix {
		t.Errorf("renewed token %q, want one for %s", renewed["AWS_SESSION_TOKEN"], prefix)
	}
	if _, ok := lab.mock.Secrets["org-my/"+k8s.BackupCredentialsSecretName]; ok {
		t.Error("a CNPG credential was written for a MySQL project")
	}
}
