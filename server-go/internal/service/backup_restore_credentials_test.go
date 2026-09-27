package service

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/objectcreds"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestARestoreReadsItsSourceReadOnlyAndArchivesWithItsOwnCredentials(t *testing.T) {
	mock := k8s.NewMockClient()
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})
	adapter.SetRestorePlanSource(backedUpEnterprisePlan())
	minter := &recordingMinter{}
	adapter.SetBackupCredentials(newTestIssuer(t, minter, newFakeObjectDeleter()))

	if _, err := adapter.Restore(context.Background(), sourceInstance(), domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if leaked := secretsHolding(mock, r2Storage().SecretAccessKey); len(leaked) != 0 {
		t.Fatalf("the platform key reached %v", leaked)
	}
	source := projectSecret(t, mock, "org-dst", k8s.RecoverySourceCredentialsSecretName)
	own := projectSecret(t, mock, "org-dst", k8s.BackupCredentialsSecretName)
	sourcePrefix := sourceInstance().ProjectID + "/cloud/"
	if string(source[k8s.BackupCredentialsSessionTokenKey]) != "token-"+sourcePrefix || string(source["ACCESS_SECRET_KEY"]) != "tmp-secret-"+string(objectcreds.ReadOnly) {
		t.Errorf("the source must be read with read-only credentials for its prefix, got %v", source)
	}
	if string(own[k8s.BackupCredentialsSessionTokenKey]) != "token-dst/cloud/" || string(own["ACCESS_SECRET_KEY"]) != "tmp-secret-"+string(objectcreds.ReadWrite) {
		t.Errorf("the restored project archives with its own read-write credentials, got %v", own)
	}
	for _, scope := range minter.minted() {
		if scope.Access == objectcreds.ReadOnly && scope.TTL != 2*time.Hour {
			t.Errorf("the source credential lives %s, want the short restore lifetime", scope.TTL)
		}
	}
	recovery := mock.CRDs["org-dst/"+k8s.RecoverySourceObjectStoreName("dst")]
	name, _, _ := unstructured.NestedString(recovery.Object, "spec", "configuration", "s3Credentials", "secretAccessKey", "name")
	if name != k8s.RecoverySourceCredentialsSecretName {
		t.Errorf("the recovery store must read the source credentials, got %q", name)
	}
	ownStore := mock.CRDs["org-dst/"+k8s.BackupObjectStoreName("dst")]
	name, _, _ = unstructured.NestedString(ownStore.Object, "spec", "configuration", "s3Credentials", "secretAccessKey", "name")
	if name != k8s.BackupCredentialsSecretName {
		t.Errorf("the restored project's own store must use its own credentials, got %q", name)
	}
}

func TestARestoreWithoutBackupsGetsOnlyTheSourceCredential(t *testing.T) {
	mock := k8s.NewMockClient()
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})
	adapter.SetBackupCredentials(newTestIssuer(t, &recordingMinter{}, newFakeObjectDeleter()))

	if _, err := adapter.Restore(context.Background(), sourceInstance(), domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, ok := mock.Secrets["org-dst/"+k8s.BackupCredentialsSecretName]; ok {
		t.Error("a restored project that archives nowhere needs no write credential")
	}
	projectSecret(t, mock, "org-dst", k8s.RecoverySourceCredentialsSecretName)
}

func TestARestoreIsRefusedBeforeAnythingExistsWhenItCannotGetCredentials(t *testing.T) {
	for name, configure := range map[string]func(*K8sBackupAdapter){
		"no provider": func(a *K8sBackupAdapter) { a.SetBackupCredentials(nil) },
		"mint fails": func(a *K8sBackupAdapter) {
			a.SetBackupCredentials(newTestIssuer(t, &recordingMinter{err: errors.New("down")}, newFakeObjectDeleter()))
		},
		"target prefix in use": func(a *K8sBackupAdapter) {
			a.SetBackupCredentials(newTestIssuer(t, &recordingMinter{}, newFakeObjectDeleter("dst/cloud/wals/1")))
		},
	} {
		t.Run(name, func(t *testing.T) {
			mock := k8s.NewMockClient()
			adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})
			adapter.SetRestorePlanSource(backedUpEnterprisePlan())
			configure(adapter)
			if _, err := adapter.Restore(context.Background(), sourceInstance(), domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"}); err == nil {
				t.Fatal("restore must be refused")
			}
			if slices.ContainsFunc(mock.Calls, func(call string) bool {
				return call == "CreateProjectNamespace:org-dst" || call == "ApplyCRD:org-dst/dst-postgres"
			}) {
				t.Errorf("nothing may be created: %v", mock.Calls)
			}
		})
	}
}

func temporaryCredentials() *domain.S3Credentials {
	return &domain.S3Credentials{AccessKeyID: "tmp", SecretAccessKey: "tmp-secret", SessionToken: "tmp-token", ExpiresAt: time.Now().Add(time.Hour)}
}
