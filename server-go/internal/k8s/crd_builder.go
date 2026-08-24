package k8s

import (
	"fmt"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	cnpgGroup      = "postgresql.cnpg.io"
	cnpgAPIVersion = "postgresql.cnpg.io/v1"
	postgresSuffix = "-postgres"
)


// GVRs for CloudNativePG CRDs.
var (
	CNPGClusterGVR = schema.GroupVersionResource{
		Group:    cnpgGroup,
		Version:  "v1",
		Resource: "clusters",
	}
	CNPGScheduledBackupGVR = schema.GroupVersionResource{
		Group:    cnpgGroup,
		Version:  "v1",
		Resource: "scheduledbackups",
	}
	CNPGBackupGVR = schema.GroupVersionResource{
		Group:    cnpgGroup,
		Version:  "v1",
		Resource: "backups",
	}
)

type PostgreSQLClusterOpts struct {
	ProjectID       string
	Namespace       string
	Tier            config.TierConfig
	Backup          *BackupOpts
	StorageClass    string
	PostgresVersion string
	DatabaseName    string
	MasterUsername  string
	Parameters      map[string]string
	Tags            map[string]string
}

type BackupOpts struct {
	Schedule      string
	RetentionDays int
	// EndpointURL is the S3-compatible storage endpoint Barman writes to.
	// For Cloudflare R2: https://<account_id>.r2.cloudflarestorage.com
	// For AWS S3: empty (Barman defaults to AWS).
	// For local dev: http://floci.excalibase-platform.svc.cluster.local:4566
	// Empty string keeps the historical localstack default for backwards compat.
	EndpointURL string
	// Bucket is the destination bucket name. Path within the bucket is
	// automatically scoped to the project: s3://<bucket>/<projectID>/...
	// Empty defaults to "postgres-backups" (legacy default).
	Bucket string
	// SecretName is the K8s Secret holding ACCESS_KEY_ID + ACCESS_SECRET_KEY.
	// Same field names work for AWS S3, R2, MinIO, floci/localstack — Barman
	// is provider-agnostic at this layer.
	SecretName string
}

// BuildPostgreSQLCluster builds a CloudNativePG Cluster CRD as unstructured.
func BuildPostgreSQLCluster(opts PostgreSQLClusterOpts) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": cnpgAPIVersion,
			"kind":       "Cluster",
			"metadata":   buildClusterMetadata(opts),
			"spec":       buildClusterSpec(opts),
		},
	}
}

// buildClusterMetadata builds the metadata section of a CNPG Cluster CRD.
func buildClusterMetadata(opts PostgreSQLClusterOpts) map[string]interface{} {
	labels := map[string]interface{}{}
	for k, v := range opts.Tags {
		labels[k] = v
	}

	metadata := map[string]interface{}{
		"name":      opts.ProjectID + postgresSuffix,
		"namespace": opts.Namespace,
	}
	if len(labels) > 0 {
		metadata["labels"] = labels
	}
	return metadata
}

// buildClusterSpec builds the spec section of a CNPG Cluster CRD.
func buildClusterSpec(opts PostgreSQLClusterOpts) map[string]interface{} {
	dbName := "app"
	dbUser := "app"
	if opts.DatabaseName != "" {
		dbName = opts.DatabaseName
	}
	if opts.MasterUsername != "" {
		dbUser = opts.MasterUsername
	}

	postgresql, storage := buildPostgresqlAndStorage(opts)

	// enablePodMonitor is left off entirely. CNPG's PodMonitor reconciler
	// blows up with "cannot create Cluster auxiliary objects: expected
	// pointer, but got invalid kind" when the Prometheus operator's CRDs are
	// installed but Prometheus isn't scraping the project namespace — which
	// is the typical AIO setup. Operators who want pod metrics scraped can
	// add a PodMonitor out-of-band; we don't need to bake it into the CRD.
	//
	// customQueriesConfigMap also dropped: CNPG looks for the configMap in
	// the project namespace; we only ship it to cnpg-system + platform.
	// Reintroducing means copying the configMap on namespace creation,
	// which we can do later if anyone actually consumes those queries.
	monitoring := map[string]interface{}{
		"enablePodMonitor": false,
	}

	spec := map[string]interface{}{
		"instances":  int64(opts.Tier.Instances),
		"storage":    storage,
		"postgresql": postgresql,
		"monitoring": monitoring,
		"resources": map[string]interface{}{
			"requests": map[string]interface{}{
				"memory": opts.Tier.Memory,
				"cpu":    opts.Tier.CPU,
			},
			"limits": map[string]interface{}{
				"memory": opts.Tier.Memory,
				"cpu":    opts.Tier.CPU,
			},
		},
	}

	if dbName != "app" || dbUser != "app" {
		spec["bootstrap"] = map[string]interface{}{
			"initdb": map[string]interface{}{
				"database": dbName,
				"owner":    dbUser,
			},
		}
	}

	if opts.Backup != nil {
		spec["backup"] = buildBackupSpec(opts.ProjectID, opts.Backup)
	}

	if opts.PostgresVersion != "" {
		spec["imageName"] = fmt.Sprintf("ghcr.io/cloudnative-pg/postgresql:%s", opts.PostgresVersion)
	}

	return spec
}

// buildPostgresqlAndStorage builds the postgresql config and storage sections.
func buildPostgresqlAndStorage(opts PostgreSQLClusterOpts) (map[string]interface{}, map[string]interface{}) {
	params := map[string]interface{}{
		"max_connections": "100",
	}
	var sharedPreloadLibs []interface{}
	for k, v := range opts.Parameters {
		if k == "shared_preload_libraries" {
			for _, lib := range strings.Split(v, ",") {
				sharedPreloadLibs = append(sharedPreloadLibs, strings.TrimSpace(lib))
			}
		} else {
			params[k] = v
		}
	}

	postgresql := map[string]interface{}{
		"parameters": params,
		// CNPG's default pg_hba only permits the internal streaming_replica user
		// over TLS with client cert auth. We grant replication to a dedicated
		// cdc_watcher role (created post-bootstrap by createProjectRoles) and
		// regular client access to app + excalibase_app + auth_admin via
		// password auth.
		"pg_hba": []interface{}{
			"host replication cdc_watcher all scram-sha-256",
			"host all app all scram-sha-256",
			"host all excalibase_app all scram-sha-256",
			"host all auth_admin all scram-sha-256",
		},
	}
	if len(sharedPreloadLibs) > 0 {
		postgresql["shared_preload_libraries"] = sharedPreloadLibs
	}

	storage := map[string]interface{}{"size": opts.Tier.StorageSize}
	if opts.StorageClass != "" {
		storage["storageClass"] = opts.StorageClass
	}

	return postgresql, storage
}

// buildBackupSpec builds the backup section of the CNPG Cluster spec.
// Provider-agnostic: works for AWS S3, Cloudflare R2, MinIO, floci. The
// caller picks via BackupOpts{EndpointURL, Bucket, SecretName} — all 3
// have legacy defaults so existing callers don't break.
func buildBackupSpec(projectID string, backup *BackupOpts) map[string]interface{} {
	bucket := backup.Bucket
	if bucket == "" {
		bucket = "postgres-backups"
	}
	endpoint := backup.EndpointURL
	if endpoint == "" {
		// Legacy default for dev/test that still runs against localstack/floci.
		// Production should always set EndpointURL via chart values.
		endpoint = "http://floci.excalibase-platform.svc.cluster.local:4566"
	}
	secret := backup.SecretName
	if secret == "" {
		secret = "backup-s3-creds"
	}
	return map[string]interface{}{
		"retentionPolicy": fmt.Sprintf("%dd", backup.RetentionDays),
		"barmanObjectStore": map[string]interface{}{
			"serverName":      "cloud",
			"destinationPath": fmt.Sprintf("s3://%s/%s", bucket, projectID),
			"endpointURL":     endpoint,
			"s3Credentials": map[string]interface{}{
				"accessKeyId": map[string]interface{}{
					"name": secret,
					"key":  "ACCESS_KEY_ID",
				},
				"secretAccessKey": map[string]interface{}{
					"name": secret,
					"key":  "ACCESS_SECRET_KEY",
				},
			},
			"wal": map[string]interface{}{
				"compression": "gzip",
				"maxParallel": int64(2),
			},
			"data": map[string]interface{}{
				"compression": "gzip",
			},
		},
	}
}

// BuildScheduledBackup builds a CNPG ScheduledBackup CRD.
func BuildScheduledBackup(projectID, namespace, schedule string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": cnpgAPIVersion,
			"kind":       "ScheduledBackup",
			"metadata": map[string]interface{}{
				"name":      projectID + "-postgres-backup",
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"schedule":              schedule,
				"backupOwnerReference":  "self",
				"cluster": map[string]interface{}{
					"name": projectID + postgresSuffix,
				},
				"immediate": false,
				"target":    "prefer-standby",
			},
		},
	}
}

// BuildManualBackup builds a CNPG Backup CRD for on-demand backup.
func BuildManualBackup(projectID, namespace, backupName string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": cnpgAPIVersion,
			"kind":       "Backup",
			"metadata": map[string]interface{}{
				"name":      backupName,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"cluster": map[string]interface{}{
					"name": projectID + postgresSuffix,
				},
			},
		},
	}
}
