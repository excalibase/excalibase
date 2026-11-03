package k8s

import (
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A major upgrade (EXC-668) gives the upgraded database its own Barman
// archive: pg_upgrade starts a new system at timeline 1, and the plugin does
// not separate archives itself (CNPG docs, "Backup and WAL Archive
// Considerations"). The original archive keeps its layout,
// s3://<bucket>/<project> with server "cloud"; archive "pg<major>" is
// s3://<bucket>/<project>/cloud with server "pg<major>", so every key stays
// under BarmanObjectPrefix, which the scoped credentials and the purge cover.

// cnpgHealthyPhase is the phase CNPG reports for a cluster doing nothing.
const cnpgHealthyPhase = "Cluster in healthy state"

// ArchiveObjectStoreName is the ObjectStore a cluster writes archive to.
func ArchiveObjectStoreName(projectID, archive string) string {
	if archive == "" {
		return BackupObjectStoreName(projectID)
	}
	return BackupObjectStoreName(projectID) + "-" + archive
}

// ArchiveServerName is the Barman server name of archive.
func ArchiveServerName(archive string) string {
	if archive == "" {
		return barmanServerName
	}
	return archive
}

// ArchiveDestinationPath is the destinationPath of archive's store.
func ArchiveDestinationPath(bucket, projectID, archive string) string {
	if archive == "" {
		return fmt.Sprintf("s3://%s/%s", bucket, projectID)
	}
	return fmt.Sprintf("s3://%s/%s/%s", bucket, projectID, barmanServerName)
}

// BuildArchiveObjectStore copies current, the store the cluster archives to
// now, into archive's store: same bucket, credentials, retention and
// sidecar settings, its own destination. current is not modified.
func BuildArchiveObjectStore(current *unstructured.Unstructured, projectID, archive string) (*unstructured.Unstructured, error) {
	spec, _, _ := unstructured.NestedMap(current.Object, "spec")
	destination, _, _ := unstructured.NestedString(spec, "configuration", "destinationPath")
	bucket, ok := s3Bucket(destination)
	if !ok {
		return nil, fmt.Errorf("backup store %s has no s3 destination to copy", current.GetName())
	}
	copied := runtimeDeepCopy(spec)
	if err := unstructured.SetNestedField(copied, ArchiveDestinationPath(bucket, projectID, archive), "configuration", "destinationPath"); err != nil {
		return nil, err
	}
	return objectStore(ArchiveObjectStoreName(projectID, archive), current.GetNamespace(), copied), nil
}

func s3Bucket(destination string) (string, bool) {
	rest, found := strings.CutPrefix(destination, "s3://")
	if !found {
		return "", false
	}
	bucket, _, _ := strings.Cut(rest, "/")
	return bucket, bucket != ""
}

func runtimeDeepCopy(in map[string]interface{}) map[string]interface{} {
	return (&unstructured.Unstructured{Object: in}).DeepCopy().Object
}

// ClusterArchive is the store and server name the cluster archives to.
func ClusterArchive(cluster *unstructured.Unstructured) (store, server string, ok bool) {
	spec, _, _ := unstructured.NestedMap(cluster.Object, "spec")
	params, ok := walArchiveParameters(spec)
	if !ok {
		return "", "", false
	}
	store, _ = params["barmanObjectName"].(string)
	server, _ = params["serverName"].(string)
	return store, server, true
}

// SetClusterArchive points the cluster at image and its WAL archiver at
// store/server in the same object, so one update moves both.
func SetClusterArchive(cluster *unstructured.Unstructured, image, store, server string) error {
	plugins, _, _ := unstructured.NestedSlice(cluster.Object, "spec", "plugins")
	found := false
	for _, raw := range plugins {
		plugin, _ := raw.(map[string]interface{})
		if archiver, _ := plugin["isWALArchiver"].(bool); !archiver || plugin["name"] != BarmanCloudPluginName {
			continue
		}
		params, _ := plugin["parameters"].(map[string]interface{})
		if params == nil {
			params = map[string]interface{}{}
			plugin["parameters"] = params
		}
		params["barmanObjectName"] = store
		params["serverName"] = server
		found = true
	}
	if !found {
		return ErrClusterHasNoArchive
	}
	if err := unstructured.SetNestedSlice(cluster.Object, plugins, "spec", "plugins"); err != nil {
		return err
	}
	return unstructured.SetNestedField(cluster.Object, image, "spec", "imageName")
}

// ClusterDataMajor is the major the cluster's data directory is on, as CNPG
// records it (status.pgDataImageInfo.majorVersion).
func ClusterDataMajor(cluster *unstructured.Unstructured) (int64, bool) {
	major, found, err := unstructured.NestedInt64(cluster.Object, "status", "pgDataImageInfo", "majorVersion")
	return major, found && err == nil && major > 0
}

// ClusterPrimaryServing: CNPG reports the cluster healthy with at least one
// instance ready.
func ClusterPrimaryServing(cluster *unstructured.Unstructured) bool {
	phase, _, _ := unstructured.NestedString(cluster.Object, "status", "phase")
	ready, _, _ := unstructured.NestedInt64(cluster.Object, "status", "readyInstances")
	return phase == cnpgHealthyPhase && ready > 0
}

// ClusterFullyServing: healthy, with every instance ready.
func ClusterFullyServing(cluster *unstructured.Unstructured) bool {
	instances, _, _ := unstructured.NestedInt64(cluster.Object, "status", "instances")
	ready, _, _ := unstructured.NestedInt64(cluster.Object, "status", "readyInstances")
	return ClusterPrimaryServing(cluster) && instances > 0 && ready >= instances
}

// UpgradeJobState is what the cluster's pg_upgrade Job is doing.
type UpgradeJobState int

const (
	UpgradeJobAbsent UpgradeJobState = iota
	UpgradeJobRunning
	UpgradeJobFailed
)

// MajorUpgradeJobState finds cluster's major-upgrade Job among jobs (CNPG
// labels it cnpg.io/jobRole=major-upgrade; it runs once, BackoffLimit 0).
// A Job created before since belongs to an earlier attempt and is ignored.
func MajorUpgradeJobState(jobs []*unstructured.Unstructured, cluster string, since time.Time) UpgradeJobState {
	for _, job := range jobs {
		labels := job.GetLabels()
		if labels["cnpg.io/jobRole"] != "major-upgrade" || labels["cnpg.io/cluster"] != cluster {
			continue
		}
		if job.GetCreationTimestamp().Time.Before(since) {
			continue
		}
		if jobFailed(job) {
			return UpgradeJobFailed
		}
		return UpgradeJobRunning
	}
	return UpgradeJobAbsent
}

func jobFailed(job *unstructured.Unstructured) bool {
	conditions, _, _ := unstructured.NestedSlice(job.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, _ := raw.(map[string]interface{})
		if condition["type"] == "Failed" && condition["status"] == "True" {
			return true
		}
	}
	failed, _, _ := unstructured.NestedInt64(job.Object, "status", "failed")
	return failed > 0
}
