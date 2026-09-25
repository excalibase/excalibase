package k8s

import "testing"

const (
	testR2Endpoint         = "https://acct.r2.cloudflarestorage.com"
	testLocalstackEndpoint = "http://localstack.localstack.svc.cluster.local:4566"
)

func barmanStoreOf(t *testing.T, spec map[string]interface{}) map[string]interface{} {
	t.Helper()
	external := spec["externalClusters"].([]interface{})
	if len(external) != 1 {
		t.Fatalf("externalClusters: got %d entries, want 1", len(external))
	}
	return external[0].(map[string]interface{})["barmanObjectStore"].(map[string]interface{})
}

func TestBuildRestoreClusterPointsAtConfiguredStore(t *testing.T) {
	obj := BuildRestoreCluster(RestoreClusterOpts{
		SourceProjectID: "src-db",
		NewProjectID:    "dst-db",
		Namespace:       "org-dst-db",
		Store:           ObjectStoreOpts{EndpointURL: testR2Endpoint, Bucket: "excalibase-backups", SecretName: "backup-s3-creds"},
	})

	meta := obj.Object["metadata"].(map[string]interface{})
	if meta["name"] != "dst-db-postgres" || meta["namespace"] != "org-dst-db" {
		t.Errorf("metadata: got %v", meta)
	}
	spec := obj.Object["spec"].(map[string]interface{})
	recovery := spec["bootstrap"].(map[string]interface{})["recovery"].(map[string]interface{})
	if recovery["source"] != "clusterBackup" {
		t.Errorf("recovery source: got %v", recovery["source"])
	}
	if _, has := recovery["recoveryTarget"]; has {
		t.Error("recoveryTarget must be absent without a PITR target")
	}

	store := barmanStoreOf(t, spec)
	if store["endpointURL"] != testR2Endpoint {
		t.Errorf("endpointURL: got %v, want R2", store["endpointURL"])
	}
	if store["destinationPath"] != "s3://excalibase-backups/src-db" {
		t.Errorf("destinationPath: got %v", store["destinationPath"])
	}
	creds := store["s3Credentials"].(map[string]interface{})
	if creds["accessKeyId"].(map[string]interface{})["name"] != "backup-s3-creds" {
		t.Errorf("secret name: got %v", creds["accessKeyId"])
	}
}

func TestBuildRestoreClusterOmitsEndpointWhenUnset(t *testing.T) {
	obj := BuildRestoreCluster(RestoreClusterOpts{
		SourceProjectID: "src-db",
		NewProjectID:    "dst-db",
		Namespace:       "ns",
		Store:           ObjectStoreOpts{Bucket: "b", SecretName: "s"},
	})
	store := barmanStoreOf(t, obj.Object["spec"].(map[string]interface{}))
	if endpoint, has := store["endpointURL"]; has {
		t.Errorf("endpointURL must be omitted (Barman defaults to AWS S3), got %v", endpoint)
	}
}

func TestBuildRestoreClusterCarriesRecoveryTarget(t *testing.T) {
	obj := BuildRestoreCluster(RestoreClusterOpts{
		SourceProjectID: "src-db",
		NewProjectID:    "dst-db",
		Namespace:       "ns",
		Store:           ObjectStoreOpts{EndpointURL: testR2Endpoint, Bucket: "b", SecretName: "s"},
		RecoveryTarget:  map[string]interface{}{"targetTime": "2026-01-01T00:00:00Z"},
	})
	recovery := obj.Object["spec"].(map[string]interface{})["bootstrap"].(map[string]interface{})["recovery"].(map[string]interface{})
	target := recovery["recoveryTarget"].(map[string]interface{})
	if target["targetTime"] != "2026-01-01T00:00:00Z" {
		t.Errorf("recoveryTarget: got %v", target)
	}
}

func TestBuildBackupSpecNeverDefaultsToLocalstack(t *testing.T) {
	spec := buildBackupSpec("p1", &BackupOpts{RetentionDays: 7, Bucket: "b"})
	store := spec["barmanObjectStore"].(map[string]interface{})
	if endpoint, has := store["endpointURL"]; has {
		t.Errorf("endpointURL must be omitted when unset, got %v", endpoint)
	}
}

func TestBuildBackupSpecHonoursExplicitLocalstack(t *testing.T) {
	spec := buildBackupSpec("p1", &BackupOpts{RetentionDays: 7, Bucket: "b", EndpointURL: testLocalstackEndpoint})
	store := spec["barmanObjectStore"].(map[string]interface{})
	if store["endpointURL"] != testLocalstackEndpoint {
		t.Errorf("explicit localstack endpoint must be kept, got %v", store["endpointURL"])
	}
}

func TestBuildRestoreClusterNamesTheImage(t *testing.T) {
	image := "excalibase/postgresql:16@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	obj := BuildRestoreCluster(RestoreClusterOpts{
		SourceProjectID: "src", NewProjectID: "dst", Namespace: "org-dst", ImageName: image,
	})
	spec := obj.Object["spec"].(map[string]interface{})
	if spec["imageName"] != image {
		t.Errorf("imageName: got %v, want %s", spec["imageName"], image)
	}
}
