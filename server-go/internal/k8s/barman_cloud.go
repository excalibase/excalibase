package k8s

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// BarmanCloudPluginName is the CNPG-I plugin every backup, WAL segment and
// recovery goes through. Its sidecar carries barman-cloud, so backups do not
// depend on what the Postgres image contains.
const BarmanCloudPluginName = "barman-cloud.cloudnative-pg.io"

// ObjectStoreGVR is the plugin's object store resource.
var ObjectStoreGVR = schema.GroupVersionResource{Group: "barmancloud.cnpg.io", Version: "v1", Resource: "objectstores"}

// ErrObjectStoreIncomplete refuses a store with no bucket or no credentials:
// there is no default place a project's backups could go.
var ErrObjectStoreIncomplete = errors.New("backup object store needs a bucket and a credentials secret")

const (
	barmanCloudAPIVersion = "barmancloud.cnpg.io/v1"
	recoverySourceName    = "clusterBackup"
	// barmanServerName is the Barman server name under destinationPath;
	// every base backup and WAL segment lands beneath it.
	barmanServerName = "cloud"
	// backupSessionOptions lifts the tier's timeouts for the backup tool's own
	// Postgres sessions only.
	backupSessionOptions = "-c statement_timeout=0 -c idle_in_transaction_session_timeout=0"
)

// BackupObjectStoreName is the store a project's cluster archives to.
func BackupObjectStoreName(projectID string) string { return projectID + "-backups" }

// RecoverySourceObjectStoreName is the store a restored project reads its
// source's backups from. It lives in the restored project's namespace.
func RecoverySourceObjectStoreName(projectID string) string { return projectID + "-recovery-source" }

// BarmanObjectPrefix is the object-key prefix (relative to the bucket) that
// holds everything the plugin wrote for a project: destinationPath/serverName/.
// The deprovision purge deletes exactly this prefix.
func BarmanObjectPrefix(projectID string) string {
	return projectID + "/" + barmanServerName + "/"
}

// BackupCredentialsSecretName is the Secret in a project namespace holding
// the object store's ACCESS_KEY_ID and ACCESS_SECRET_KEY.
const BackupCredentialsSecretName = "backup-s3-creds"

// Store is where these backups are written.
func (b *BackupOpts) Store() ObjectStoreOpts {
	return ObjectStoreOpts{EndpointURL: b.EndpointURL, Bucket: b.Bucket, SecretName: b.SecretName}
}

// BuildBackupObjectStore is where a project's WAL and base backups go, kept
// for retentionDays.
func BuildBackupObjectStore(projectID, namespace string, store ObjectStoreOpts, retentionDays int) (*unstructured.Unstructured, error) {
	configuration, err := objectStoreConfiguration(projectID, store)
	if err != nil {
		return nil, err
	}
	configuration["wal"] = map[string]interface{}{"compression": "gzip", "maxParallel": int64(2)}
	configuration["data"] = map[string]interface{}{"compression": "gzip"}
	spec := map[string]interface{}{
		"configuration": configuration,
		// The tier's query guard is cluster-wide, and pg_backup_start waits
		// for a spread checkpoint far longer than any tier's timeout.
		"instanceSidecarConfiguration": map[string]interface{}{
			"env": []interface{}{map[string]interface{}{"name": "PGOPTIONS", "value": backupSessionOptions}},
		},
	}
	if retentionDays > 0 {
		spec["retentionPolicy"] = fmt.Sprintf("%dd", retentionDays)
	}
	return objectStore(BackupObjectStoreName(projectID), namespace, spec), nil
}

// BuildRecoverySourceObjectStore points at the source project's backups. It
// has no retention policy: only a cluster archiving to a store prunes it, and
// the restored project must never prune its source.
func BuildRecoverySourceObjectStore(opts RestoreClusterOpts) (*unstructured.Unstructured, error) {
	configuration, err := objectStoreConfiguration(opts.SourceProjectID, opts.Store)
	if err != nil {
		return nil, err
	}
	configuration["wal"] = map[string]interface{}{"maxParallel": int64(8)}
	name := RecoverySourceObjectStoreName(opts.Cluster.ProjectID)
	return objectStore(name, opts.Cluster.Namespace, map[string]interface{}{"configuration": configuration}), nil
}

// objectStoreConfiguration is the one place a store's location and
// credentials are shaped, so backup (write), restore (read) and purge
// (delete) agree. serverName stays empty: the plugin takes it from the
// cluster's parameters.
func objectStoreConfiguration(projectID string, store ObjectStoreOpts) (map[string]interface{}, error) {
	if store.Bucket == "" || store.SecretName == "" {
		return nil, ErrObjectStoreIncomplete
	}
	configuration := map[string]interface{}{
		"destinationPath": fmt.Sprintf("s3://%s/%s", store.Bucket, projectID),
		"s3Credentials": map[string]interface{}{
			"accessKeyId":     map[string]interface{}{"name": store.SecretName, "key": "ACCESS_KEY_ID"},
			"secretAccessKey": map[string]interface{}{"name": store.SecretName, "key": "ACCESS_SECRET_KEY"},
		},
	}
	if store.EndpointURL != "" {
		configuration["endpointURL"] = store.EndpointURL
	}
	return configuration, nil
}

func objectStore(name, namespace string, spec map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": barmanCloudAPIVersion,
		"kind":       "ObjectStore",
		"metadata":   map[string]interface{}{"name": name, "namespace": namespace},
		"spec":       spec,
	}}
}

func barmanPluginParameters(storeName string) map[string]interface{} {
	return map[string]interface{}{"barmanObjectName": storeName, "serverName": barmanServerName}
}

// backupPlugin makes the plugin the cluster's WAL archiver and backup method.
func backupPlugin(projectID string) map[string]interface{} {
	return map[string]interface{}{
		"name":          BarmanCloudPluginName,
		"enabled":       true,
		"isWALArchiver": true,
		"parameters":    barmanPluginParameters(BackupObjectStoreName(projectID)),
	}
}

func recoverySource(projectID string) map[string]interface{} {
	return map[string]interface{}{
		"name": recoverySourceName,
		"plugin": map[string]interface{}{
			"name":       BarmanCloudPluginName,
			"parameters": barmanPluginParameters(RecoverySourceObjectStoreName(projectID)),
		},
	}
}

func pluginBackupSpec(clusterName string) map[string]interface{} {
	return map[string]interface{}{
		"cluster":             map[string]interface{}{"name": clusterName},
		"method":              "plugin",
		"pluginConfiguration": map[string]interface{}{"name": BarmanCloudPluginName},
	}
}
