package k8s

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestObjectStoresPresentTheSessionTokenOfTemporaryCredentials(t *testing.T) {
	backup, err := BuildBackupObjectStore("proj-bkp", "org-proj-bkp", projectStore(), 7)
	if err != nil {
		t.Fatalf("BuildBackupObjectStore: %v", err)
	}
	source, err := BuildRecoverySourceObjectStore(restoreOf(clusterOpts("dst", "org-dst")))
	if err != nil {
		t.Fatalf("BuildRecoverySourceObjectStore: %v", err)
	}
	for _, store := range []*unstructured.Unstructured{backup, source} {
		secret, _, _ := unstructured.NestedString(store.Object, "spec", "configuration", "s3Credentials", "secretAccessKey", "name")
		token, _, _ := unstructured.NestedStringMap(store.Object, "spec", "configuration", "s3Credentials", "sessionToken")
		if token["name"] != secret || token["key"] != BackupCredentialsSessionTokenKey {
			t.Errorf("%s: sessionToken must come from the credentials secret, got %v", store.GetName(), token)
		}
	}
}

func TestClustersThatArchiveSkipTheBucketLevelArchiveCheck(t *testing.T) {
	// Temporary credentials cannot HeadBucket, which barman-cloud-check-wal-archive
	// needs; the platform checks the prefix is empty itself before minting.
	restored := mustBuildRestore(t, restoreOf(fullProjectCluster()))
	for name, cluster := range map[string]*unstructured.Unstructured{
		"new project": BuildPostgreSQLCluster(backedUpCluster()),
		"restored":    restored,
	} {
		if got := cluster.GetAnnotations()[SkipEmptyWalArchiveCheckAnnotation]; got != "enabled" {
			t.Errorf("%s: annotation %s = %q, want enabled", name, SkipEmptyWalArchiveCheckAnnotation, got)
		}
	}
	opts := backedUpCluster()
	opts.Backup = nil
	if _, set := BuildPostgreSQLCluster(opts).GetAnnotations()[SkipEmptyWalArchiveCheckAnnotation]; set {
		t.Error("a cluster that archives nowhere has no archive check to skip")
	}
}

func TestBackupCredentialsSecretCarriesTheTemporaryCredentialAndItsExpiry(t *testing.T) {
	expires := time.Date(2026, 9, 28, 15, 0, 0, 0, time.UTC)
	data, err := BackupCredentialsSecretData(&domain.S3Credentials{
		AccessKeyID: "tmp-id", SecretAccessKey: "tmp-secret", SessionToken: "tmp-token", ExpiresAt: expires,
		IssuedBy: BackupKeyFingerprint(&domain.S3Credentials{AccessKeyID: "platform-key", SecretAccessKey: "s"}),
		Bucket:   "backups", Endpoint: "https://acct.r2.cloudflarestorage.com",
	})
	if err != nil {
		t.Fatalf("BackupCredentialsSecretData: %v", err)
	}
	want := map[string]string{
		"ACCESS_KEY_ID": "tmp-id", "ACCESS_SECRET_KEY": "tmp-secret",
		BackupCredentialsSessionTokenKey: "tmp-token", BackupCredentialsExpiresAtKey: "2026-09-28T15:00:00Z",
		BackupCredentialsIssuedByKey: BackupKeyFingerprint(&domain.S3Credentials{AccessKeyID: "platform-key", SecretAccessKey: "s"}),
		BackupCredentialsBucketKey:   "backups", BackupCredentialsEndpointKey: "https://acct.r2.cloudflarestorage.com",
	}
	key := func(id, secret string) string {
		return BackupKeyFingerprint(&domain.S3Credentials{AccessKeyID: id, SecretAccessKey: secret})
	}
	if fp := key("platform-key", "s"); len(fp) != 16 || fp == key("other-key", "s") || fp == key("platform-key", "rolled") {
		t.Errorf("fingerprint %q must be a short digest that changes with the key id or its secret", fp)
	}
	if len(data) != len(want) {
		t.Errorf("secret keys: got %d, want %d", len(data), len(want))
	}
	for key, value := range want {
		if string(data[key]) != value {
			t.Errorf("%s = %q, want %q", key, data[key], value)
		}
	}
	got, err := BackupCredentialsExpiry(data)
	if err != nil || !got.Equal(expires) {
		t.Errorf("BackupCredentialsExpiry = %v, %v", got, err)
	}
}

func TestBackupCredentialsSecretRefusesALongLivedKey(t *testing.T) {
	for name, creds := range map[string]*domain.S3Credentials{
		"no session token": {AccessKeyID: "k", SecretAccessKey: "s", ExpiresAt: time.Now().Add(time.Hour)},
		"no expiry":        {AccessKeyID: "k", SecretAccessKey: "s", SessionToken: "t"},
		"nothing":          nil,
	} {
		if _, err := BackupCredentialsSecretData(creds); !errors.Is(err, ErrLongLivedBackupKey) {
			t.Errorf("%s: err = %v, want ErrLongLivedBackupKey", name, err)
		}
	}
}

func TestUpdateSecretReplacesTheDataOfAnExistingSecretOnly(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	if err := c.UpdateSecret(ctx, testSecNS, "absent", map[string][]byte{"k": []byte("v")}); err == nil {
		t.Fatal("updating a missing secret must fail, not create it")
	}
	if err := c.CreateSecret(ctx, testSecNS, "creds", map[string][]byte{"ACCESS_KEY_ID": []byte("old"), "STALE": []byte("x")}); err != nil {
		t.Fatalf("CreateSecret: %v", err)
	}
	if err := c.UpdateSecret(ctx, testSecNS, "creds", map[string][]byte{"ACCESS_KEY_ID": []byte("new")}); err != nil {
		t.Fatalf("UpdateSecret: %v", err)
	}
	data, err := c.GetSecret(ctx, testSecNS, "creds")
	if err != nil || string(data["ACCESS_KEY_ID"]) != "new" || data["STALE"] != nil {
		t.Errorf("secret after update = %v, %v", data, err)
	}
}

func TestBackupCredentialsExpiryOfASecretWithoutOne(t *testing.T) {
	if _, err := BackupCredentialsExpiry(map[string][]byte{"ACCESS_KEY_ID": []byte("k")}); err == nil {
		t.Error("a secret that names no expiry must not read as valid forever")
	}
}
