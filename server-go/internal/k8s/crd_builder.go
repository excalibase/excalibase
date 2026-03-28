package k8s

import (
	"fmt"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GVRs for CloudNativePG CRDs.
var (
	CNPGClusterGVR = schema.GroupVersionResource{
		Group:    "postgresql.cnpg.io",
		Version:  "v1",
		Resource: "clusters",
	}
	CNPGScheduledBackupGVR = schema.GroupVersionResource{
		Group:    "postgresql.cnpg.io",
		Version:  "v1",
		Resource: "scheduledbackups",
	}
	CNPGBackupGVR = schema.GroupVersionResource{
		Group:    "postgresql.cnpg.io",
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
}

// BuildPostgreSQLCluster builds a CloudNativePG Cluster CRD as unstructured.
func BuildPostgreSQLCluster(opts PostgreSQLClusterOpts) *unstructured.Unstructured {
	dbName := "app"
	dbUser := "app"
	if opts.DatabaseName != "" {
		dbName = opts.DatabaseName
	}
	if opts.MasterUsername != "" {
		dbUser = opts.MasterUsername
	}

	// Build parameters
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

	// Build storage
	storage := map[string]interface{}{"size": opts.Tier.StorageSize}
	if opts.StorageClass != "" {
		storage["storageClass"] = opts.StorageClass
	}

	// Build labels
	labels := map[string]interface{}{}
	for k, v := range opts.Tags {
		labels[k] = v
	}

	// Build postgresql section
	postgresql := map[string]interface{}{
		"parameters": params,
	}
	if len(sharedPreloadLibs) > 0 {
		postgresql["shared_preload_libraries"] = sharedPreloadLibs
	}

	// Monitoring: enable PodMonitor for multi-instance tiers
	enablePodMonitor := opts.Tier.Instances > 1
	monitoring := map[string]interface{}{
		"enablePodMonitor": enablePodMonitor,
		"customQueriesConfigMap": []interface{}{
			map[string]interface{}{
				"key":  "queries",
				"name": "cnpg-default-monitoring",
			},
		},
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

	// Bootstrap with custom db/user
	if dbName != "app" || dbUser != "app" {
		spec["bootstrap"] = map[string]interface{}{
			"initdb": map[string]interface{}{
				"database": dbName,
				"owner":    dbUser,
			},
		}
	}

	// Backup config
	if opts.Backup != nil {
		spec["backup"] = map[string]interface{}{
			"retentionPolicy": fmt.Sprintf("%dd", opts.Backup.RetentionDays),
			"barmanObjectStore": map[string]interface{}{
				"serverName":      "cloud",
				"destinationPath": fmt.Sprintf("s3://postgres-backups/%s", opts.ProjectID),
				"endpointURL":     "http://localstack.localstack.svc.cluster.local:4566",
				"s3Credentials": map[string]interface{}{
					"accessKeyId": map[string]interface{}{
						"name": "backup-s3-creds",
						"key":  "ACCESS_KEY_ID",
					},
					"secretAccessKey": map[string]interface{}{
						"name": "backup-s3-creds",
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

	// PostgreSQL version
	if opts.PostgresVersion != "" {
		spec["imageName"] = fmt.Sprintf("ghcr.io/cloudnative-pg/postgresql:%s", opts.PostgresVersion)
	}

	metadata := map[string]interface{}{
		"name":      opts.ProjectID + "-postgres",
		"namespace": opts.Namespace,
	}
	if len(labels) > 0 {
		metadata["labels"] = labels
	}

	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "postgresql.cnpg.io/v1",
			"kind":       "Cluster",
			"metadata":   metadata,
			"spec":       spec,
		},
	}
}

// BuildScheduledBackup builds a CNPG ScheduledBackup CRD.
func BuildScheduledBackup(projectID, namespace, schedule string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "postgresql.cnpg.io/v1",
			"kind":       "ScheduledBackup",
			"metadata": map[string]interface{}{
				"name":      projectID + "-postgres-backup",
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"schedule":              schedule,
				"backupOwnerReference":  "self",
				"cluster": map[string]interface{}{
					"name": projectID + "-postgres",
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
			"apiVersion": "postgresql.cnpg.io/v1",
			"kind":       "Backup",
			"metadata": map[string]interface{}{
				"name":      backupName,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"cluster": map[string]interface{}{
					"name": projectID + "-postgres",
				},
			},
		},
	}
}
