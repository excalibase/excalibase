package k8s

import "testing"

const (
	testR2Endpoint         = "https://acct.r2.cloudflarestorage.com"
	testLocalstackEndpoint = "http://localstack.localstack.svc.cluster.local:4566"
)

func recoverySourceOf(t *testing.T, opts RestoreClusterOpts) map[string]interface{} {
	t.Helper()
	store, err := BuildRecoverySourceObjectStore(opts)
	if err != nil {
		t.Fatalf("BuildRecoverySourceObjectStore: %v", err)
	}
	return store.Object["spec"].(map[string]interface{})["configuration"].(map[string]interface{})
}

func TestBuildRestoreClusterPointsAtConfiguredStore(t *testing.T) {
	opts := RestoreClusterOpts{
		SourceProjectID: "src-db",
		Cluster:         clusterOpts("dst-db", "org-dst-db"),
		Store:           ObjectStoreOpts{EndpointURL: testR2Endpoint, Bucket: "excalibase-backups", SecretName: "backup-s3-creds"},
	}
	obj := mustBuildRestore(t, opts)

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

	store := recoverySourceOf(t, opts)
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
	store := recoverySourceOf(t, RestoreClusterOpts{
		SourceProjectID: "src-db",
		Cluster:         clusterOpts("dst-db", "ns"),
		Store:           ObjectStoreOpts{Bucket: "b", SecretName: "s"},
	})
	if endpoint, has := store["endpointURL"]; has {
		t.Errorf("endpointURL must be omitted (Barman defaults to AWS S3), got %v", endpoint)
	}
}

func TestBuildRestoreClusterCarriesRecoveryTarget(t *testing.T) {
	obj := mustBuildRestore(t, RestoreClusterOpts{
		SourceProjectID: "src-db",
		Cluster:         clusterOpts("dst-db", "ns"),
		Store:           ObjectStoreOpts{EndpointURL: testR2Endpoint, Bucket: "b", SecretName: "s"},
		RecoveryTarget:  map[string]interface{}{"targetTime": "2026-01-01T00:00:00Z"},
	})
	recovery := obj.Object["spec"].(map[string]interface{})["bootstrap"].(map[string]interface{})["recovery"].(map[string]interface{})
	target := recovery["recoveryTarget"].(map[string]interface{})
	if target["targetTime"] != "2026-01-01T00:00:00Z" {
		t.Errorf("recoveryTarget: got %v", target)
	}
}

func backupStoreConfiguration(t *testing.T, store ObjectStoreOpts) map[string]interface{} {
	t.Helper()
	obj, err := BuildBackupObjectStore("p1", "ns", store, 7)
	if err != nil {
		t.Fatalf("BuildBackupObjectStore: %v", err)
	}
	return obj.Object["spec"].(map[string]interface{})["configuration"].(map[string]interface{})
}

func TestBackupStoreNeverDefaultsToLocalstack(t *testing.T) {
	store := backupStoreConfiguration(t, ObjectStoreOpts{Bucket: "b", SecretName: "s"})
	if endpoint, has := store["endpointURL"]; has {
		t.Errorf("endpointURL must be omitted when unset, got %v", endpoint)
	}
}

func TestBackupStoreHonoursExplicitLocalstack(t *testing.T) {
	store := backupStoreConfiguration(t, ObjectStoreOpts{Bucket: "b", SecretName: "s", EndpointURL: testLocalstackEndpoint})
	if store["endpointURL"] != testLocalstackEndpoint {
		t.Errorf("explicit localstack endpoint must be kept, got %v", store["endpointURL"])
	}
}

func TestBuildRestoreClusterNamesTheImage(t *testing.T) {
	image := "excalibase/postgresql:16@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cluster := clusterOpts("dst", "org-dst")
	cluster.ImageName = image
	obj := mustBuildRestore(t, RestoreClusterOpts{SourceProjectID: "src", Cluster: cluster, Store: projectStore()})
	spec := obj.Object["spec"].(map[string]interface{})
	if spec["imageName"] != image {
		t.Errorf("imageName: got %v, want %s", spec["imageName"], image)
	}
}

func TestBuildRestoreClusterNamesThePublicHost(t *testing.T) {
	cluster := clusterOpts("dst", "org-dst")
	cluster.ServerAltDNSNames = []string{"dst.db.example.com"}
	obj := mustBuildRestore(t, RestoreClusterOpts{SourceProjectID: "src", Cluster: cluster, Store: projectStore()})
	spec := obj.Object["spec"].(map[string]interface{})
	certificates, ok := spec["certificates"].(map[string]interface{})
	if !ok {
		t.Fatalf("no certificates section: %v", spec)
	}
	names, _ := certificates["serverAltDNSNames"].([]interface{})
	if len(names) != 1 || names[0] != "dst.db.example.com" {
		t.Errorf("serverAltDNSNames: got %v", certificates["serverAltDNSNames"])
	}
}
