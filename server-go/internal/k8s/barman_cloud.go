package k8s

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"

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
// the temporary credential its cluster archives with.
const BackupCredentialsSecretName = "backup-s3-creds"

// RecoverySourceCredentialsSecretName holds a restored project's read-only
// credential for its source's prefix.
const RecoverySourceCredentialsSecretName = "backup-source-creds"

const (
	// BackupCredentialsSessionTokenKey is the session token of the temporary
	// credential; without it the key and secret are worthless.
	BackupCredentialsSessionTokenKey = "ACCESS_SESSION_TOKEN"
	// BackupCredentialsExpiresAtKey is when the credential stops working
	// (RFC 3339). Renewal reads it; the plugin ignores it.
	BackupCredentialsExpiresAtKey = "EXPIRES_AT"
	// BackupCredentialsIssuedByKey fingerprints the platform key the
	// credential derives from: revoking that key kills the credential, so a
	// rotation is renewed at once.
	BackupCredentialsIssuedByKey = "ISSUED_BY"
	// BackupCredentialsBucketKey and BackupCredentialsEndpointKey name the
	// store the credential works against; renewal refuses to mint for another.
	BackupCredentialsBucketKey   = "BUCKET"
	BackupCredentialsEndpointKey = "ENDPOINT"
	// SkipEmptyWalArchiveCheckAnnotation turns off barman-cloud-check-wal-archive,
	// which calls HeadBucket: a prefix-scoped temporary credential cannot.
	// The platform proves the prefix empty itself before minting.
	SkipEmptyWalArchiveCheckAnnotation = "cnpg.io/skipEmptyWalArchiveCheck"
)

// ErrLongLivedBackupKey refuses to write an object-store key without a
// session token and expiry into a tenant namespace: only temporary
// credentials may leave the platform (EXC-476).
var ErrLongLivedBackupKey = errors.New("refusing to place a long-lived object-store key in a project namespace")

// BackupCredentialsSecretData is the Secret content for a temporary credential.
func BackupCredentialsSecretData(creds *domain.S3Credentials) (map[string][]byte, error) {
	if creds == nil || creds.SessionToken == "" || creds.ExpiresAt.IsZero() {
		return nil, ErrLongLivedBackupKey
	}
	return map[string][]byte{
		"ACCESS_KEY_ID":                  []byte(creds.AccessKeyID),
		"ACCESS_SECRET_KEY":              []byte(creds.SecretAccessKey),
		BackupCredentialsSessionTokenKey: []byte(creds.SessionToken),
		BackupCredentialsExpiresAtKey:    []byte(creds.ExpiresAt.UTC().Format(time.RFC3339)),
		BackupCredentialsIssuedByKey:     []byte(creds.IssuedBy),
		BackupCredentialsBucketKey:       []byte(creds.Bucket),
		BackupCredentialsEndpointKey:     []byte(creds.Endpoint),
	}, nil
}

// BackupKeyFingerprint names a platform key without revealing it. It covers
// the secret too: rolling a token keeps its id but kills what it signed.
func BackupKeyFingerprint(key *domain.S3Credentials) string {
	digest := sha256.Sum256([]byte(key.AccessKeyID + "\x00" + key.SecretAccessKey))
	return hex.EncodeToString(digest[:8])
}

// BackupCredentialsExpiry reads when a credentials Secret stops working.
func BackupCredentialsExpiry(data map[string][]byte) (time.Time, error) {
	raw, ok := data[BackupCredentialsExpiresAtKey]
	if !ok {
		return time.Time{}, fmt.Errorf("backup credentials name no %s", BackupCredentialsExpiresAtKey)
	}
	return time.Parse(time.RFC3339, string(raw))
}

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
			"sessionToken":    map[string]interface{}{"name": store.SecretName, "key": BackupCredentialsSessionTokenKey},
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

// pluginBackupSpec is every base backup the platform asks for: scheduled,
// first and manual. It runs on the primary (owner decision 2026-10-02,
// EXC-532): on a standby, pg_backup_start is cancelled by a recovery conflict
// under write load and the backup fails. One instance has only a primary.
func pluginBackupSpec(clusterName string) map[string]interface{} {
	return map[string]interface{}{
		"cluster":             map[string]interface{}{"name": clusterName},
		"method":              "plugin",
		"pluginConfiguration": map[string]interface{}{"name": BarmanCloudPluginName},
		"target":              "primary",
	}
}
