package provisioner

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	storeProject   = "bk-store"
	storeNamespace = "org1-bk-store"
)

func backedUpRequest(s3 *domain.S3Credentials) domain.ProvisioningRequest {
	return domain.ProvisioningRequest{
		PostgresVersion: "17", ProjectName: storeProject, OrgID: "org1", DBType: domain.PostgreSQL,
		Backup: &domain.BackupSettings{Enabled: true, Schedule: "0 0 2 * * *", Retention: 7, S3: s3},
	}
}

func r2Store() *domain.S3Credentials {
	return &domain.S3Credentials{AccessKeyID: "AKIA", SecretAccessKey: "secret", Bucket: "excalibase-backups", Endpoint: "https://acct.r2.cloudflarestorage.com",
		SessionToken: "token", ExpiresAt: time.Now().Add(12 * time.Hour)}
}

func TestProvisionRefusesToWriteALongLivedKeyIntoTheNamespace(t *testing.T) {
	for name, run := range provisionPaths() {
		t.Run(name, func(t *testing.T) {
			mock := k8s.NewMockClient()
			mock.SetupPostgreSQLMock(storeProject, storeNamespace, 1)
			platformKey := r2Store()
			platformKey.SessionToken, platformKey.ExpiresAt = "", time.Time{}
			if err := run(NewPostgreSQLProvisioner(mock, ""), backedUpRequest(platformKey)); !errors.Is(err, k8s.ErrLongLivedBackupKey) {
				t.Fatalf("err = %v, want ErrLongLivedBackupKey", err)
			}
			if _, written := mock.Secrets[storeNamespace+"/"+k8s.BackupCredentialsSecretName]; written {
				t.Fatal("a key without a session token reached the namespace")
			}
		})
	}
}

type provisionRun func(prov *PostgreSQLProvisioner, req domain.ProvisioningRequest) error

func provisionPaths() map[string]provisionRun {
	tier, _ := config.GetTierConfig(domain.Free)
	return map[string]provisionRun{
		"Provision": func(prov *PostgreSQLProvisioner, req domain.ProvisioningRequest) error {
			_, err := prov.Provision(context.Background(), req, tier, func(domain.ProvisioningStage) {})
			return err
		},
		"ProvisionWithRollback": func(prov *PostgreSQLProvisioner, req domain.ProvisioningRequest) error {
			_, err := prov.ProvisionWithRollback(context.Background(), req, tier, NewProvisionContext(nil, nil))
			return err
		},
	}
}

func TestProvisionCreatesTheProjectsObjectStoreBeforeItsCluster(t *testing.T) {
	for name, run := range provisionPaths() {
		t.Run(name, func(t *testing.T) {
			mock := k8s.NewMockClient()
			mock.SetupPostgreSQLMock(storeProject, storeNamespace, 1)
			if err := run(NewPostgreSQLProvisioner(mock, ""), backedUpRequest(r2Store())); err != nil {
				t.Fatalf("provision: %v", err)
			}
			store, ok := mock.CRDs[storeNamespace+"/"+k8s.BackupObjectStoreName(storeProject)]
			if !ok {
				t.Fatal("the project's ObjectStore was not created")
			}
			destination, _, _ := unstructured.NestedString(store.Object, "spec", "configuration", "destinationPath")
			secret, _, _ := unstructured.NestedString(store.Object, "spec", "configuration", "s3Credentials", "accessKeyId", "name")
			retention, _, _ := unstructured.NestedString(store.Object, "spec", "retentionPolicy")
			if destination != "s3://excalibase-backups/"+storeProject || secret != k8s.BackupCredentialsSecretName || retention != "7d" {
				t.Errorf("store: destination %q secret %q retention %q", destination, secret, retention)
			}
			storeAt := slices.Index(mock.Calls, "ApplyCRD:"+storeNamespace+"/"+k8s.BackupObjectStoreName(storeProject))
			clusterAt := slices.Index(mock.Calls, "ApplyCRD:"+storeNamespace+"/"+storeProject+"-postgres")
			if storeAt < 0 || clusterAt < 0 || storeAt > clusterAt {
				t.Errorf("the store must exist before the cluster that archives to it: %v", mock.Calls)
			}
		})
	}
}

func TestProvisionRefusesBackupsWithNowhereToPutThem(t *testing.T) {
	for name, run := range provisionPaths() {
		t.Run(name, func(t *testing.T) {
			mock := k8s.NewMockClient()
			mock.SetupPostgreSQLMock(storeProject, storeNamespace, 1)
			err := run(NewPostgreSQLProvisioner(mock, ""), backedUpRequest(nil))
			if !errors.Is(err, ErrBackupStoreMissing) {
				t.Fatalf("err: got %v, want ErrBackupStoreMissing", err)
			}
			if slices.Contains(mock.Calls, "CreateProjectNamespace:"+storeNamespace) {
				t.Error("nothing may be created for a project whose backups cannot be written")
			}
		})
	}
}

func TestProvisionReportsAMissingBackupPlugin(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.SetupPostgreSQLMock(storeProject, storeNamespace, 1)
	mock.CRDError = errors.New(`the server could not find the requested resource (post objectstores.barmancloud.cnpg.io)`)
	err := provisionPaths()["ProvisionWithRollback"](NewPostgreSQLProvisioner(mock, ""), backedUpRequest(r2Store()))
	var stage *StageError
	if !errors.As(err, &stage) || stage.Step != "create backup object store" {
		t.Fatalf("the failing step must be named, got %v", err)
	}
}
