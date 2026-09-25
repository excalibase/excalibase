package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	testR2Endpoint         = "https://acct.r2.cloudflarestorage.com"
	testLocalstackEndpoint = "http://localstack.localstack.svc.cluster.local:4566"
	testRestoreNamespace   = "org/dst"
)

func r2Storage() *domain.S3Credentials {
	return &domain.S3Credentials{
		AccessKeyID:     "r2-key",
		SecretAccessKey: "r2-secret",
		Endpoint:        testR2Endpoint,
		Bucket:          "excalibase-backups",
		Region:          "auto",
	}
}

func sourceInstance() *domain.DatabaseInstance {
	return &domain.DatabaseInstance{ProjectID: "src", OrgID: "org", Namespace: "org-src", Status: "ACTIVE", PostgresVersion: "17"}
}

func restoredBarmanStore(t *testing.T, mock *k8s.MockClient) map[string]interface{} {
	t.Helper()
	obj, ok := mock.CRDs["org-dst/dst-postgres"]
	if !ok {
		t.Fatalf("restore CRD not applied; CRDs=%v", mock.CRDs)
	}
	external, _, _ := unstructured.NestedSlice(obj.Object, "spec", "externalClusters")
	if len(external) != 1 {
		t.Fatalf("externalClusters: got %d", len(external))
	}
	return external[0].(map[string]interface{})["barmanObjectStore"].(map[string]interface{})
}

func TestK8sRestoreResolvesStoreFromBackupConfig(t *testing.T) {
	// Env must NOT influence the adapter — the backup config is the only source.
	t.Setenv("R2_ENDPOINT", testLocalstackEndpoint)
	t.Setenv("BACKUP_DEFAULT_ENDPOINT", testLocalstackEndpoint)
	t.Setenv("R2_ACCESS_KEY_ID", "env-key")

	mock := k8s.NewMockClient()
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})

	resp, err := adapter.Restore(context.Background(), sourceInstance(), domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if resp.ProjectID != "dst" || resp.Status != "ACTIVE" {
		t.Errorf("response: %+v", resp)
	}

	secret := mock.Secrets["org-dst/"+s3CredsKey]
	if string(secret["ACCESS_KEY_ID"]) != "r2-key" || string(secret["ACCESS_SECRET_KEY"]) != "r2-secret" {
		t.Errorf("secret must carry the backup config credentials, got %q", secret)
	}

	store := restoredBarmanStore(t, mock)
	if store["endpointURL"] != testR2Endpoint {
		t.Errorf("endpointURL: got %v, want %s", store["endpointURL"], testR2Endpoint)
	}
	if store["destinationPath"] != "s3://excalibase-backups/src" {
		t.Errorf("destinationPath: got %v", store["destinationPath"])
	}
}

func TestK8sRestoreUsesLocalstackOnlyWhenConfigured(t *testing.T) {
	cfg := r2Storage()
	cfg.Endpoint = testLocalstackEndpoint
	mock := k8s.NewMockClient()
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})
	adapter.storage = StaticBackupStorage(cfg)

	if _, err := adapter.Restore(context.Background(), sourceInstance(), domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if store := restoredBarmanStore(t, mock); store["endpointURL"] != testLocalstackEndpoint {
		t.Errorf("explicit localstack endpoint must be honoured, got %v", store["endpointURL"])
	}
}

func TestK8sRestoreFailsWhenStorageNotConfigured(t *testing.T) {
	t.Setenv("R2_ENDPOINT", testR2Endpoint)
	t.Setenv("R2_ACCESS_KEY_ID", "env-key")
	t.Setenv("R2_SECRET_ACCESS_KEY", "env-secret")

	for name, source := range map[string]BackupStorageSource{
		"nil source":   nil,
		"empty source": StaticBackupStorage(nil),
	} {
		t.Run(name, func(t *testing.T) {
			mock := k8s.NewMockClient()
			adapter := NewK8sBackupAdapter(mock, t.TempDir(), source)
			adapter.SetInstanceStore(emptyInstanceStore(t))
			adapter.SetProjectRegistrar(&fakeRegistrar{})

			_, err := adapter.Restore(context.Background(), sourceInstance(), domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"})
			if !errors.Is(err, ErrBackupStorageNotConfigured) {
				t.Fatalf("err: got %v, want ErrBackupStorageNotConfigured", err)
			}
			if len(mock.Namespaces) != 0 || len(mock.CRDs) != 0 {
				t.Errorf("nothing must be created without storage config: ns=%v crds=%v", mock.Namespaces, mock.CRDs)
			}
		})
	}
}

func TestK8sRestoreCarriesPITRTarget(t *testing.T) {
	mock := k8s.NewMockClient()
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})

	_, err := adapter.Restore(context.Background(), sourceInstance(), domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst", TargetName: "before-drop"})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	obj := mock.CRDs["org-dst/dst-postgres"]
	target, _, _ := unstructured.NestedMap(obj.Object, "spec", "bootstrap", "recovery", "recoveryTarget")
	if target["targetName"] != "before-drop" {
		t.Errorf("recoveryTarget: got %v", target)
	}
}

// A physical restore can only start on the major the backup was taken on,
// and only the platform's image carries the allowlisted extensions, so the
// recovery cluster names that image rather than leaving the operator default.
func TestK8sRestorePinsTheSourceMajorsImage(t *testing.T) {
	mock := k8s.NewMockClient()
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})
	src := sourceInstance()
	src.PostgresVersion = "16"

	if _, err := adapter.Restore(context.Background(), src, domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	want, err := config.PostgresImage("16")
	if err != nil {
		t.Fatalf("resolve image: %v", err)
	}
	obj := mock.CRDs["org-dst/dst-postgres"]
	if got, _, _ := unstructured.NestedString(obj.Object, "spec", "imageName"); got != want {
		t.Errorf("imageName: got %q, want %q", got, want)
	}
}

func TestK8sRestoreRefusesAnUnknownSourceMajor(t *testing.T) {
	for _, major := range []string{"", "13"} {
		mock := k8s.NewMockClient()
		adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})
		src := sourceInstance()
		src.PostgresVersion = major

		if _, err := adapter.Restore(context.Background(), src, domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"}); err == nil {
			t.Errorf("major %q: restore guessed an image", major)
		}
		if len(mock.Namespaces) != 0 || len(mock.CRDs) != 0 {
			t.Errorf("major %q: nothing may be created: ns=%v crds=%v", major, mock.Namespaces, mock.CRDs)
		}
	}
}

// A restored project is a new project with its own public name, and its
// certificate has to carry that name for verify-full to work.
func TestK8sRestoreCertificateNamesTheRestoredProjectsPublicHost(t *testing.T) {
	mock := k8s.NewMockClient()
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})
	adapter.SetPublicDomainSuffix("db.example.com")

	if _, err := adapter.Restore(context.Background(), sourceInstance(), domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	obj := mock.CRDs["org-dst/dst-postgres"]
	names, _, _ := unstructured.NestedStringSlice(obj.Object, "spec", "certificates", "serverAltDNSNames")
	if len(names) != 1 || names[0] != "dst.db.example.com" {
		t.Errorf("serverAltDNSNames: got %v", names)
	}
}
