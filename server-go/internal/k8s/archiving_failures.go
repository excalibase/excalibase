package k8s

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ArchivingFailure is a cluster whose CloudNativePG ContinuousArchiving
// condition is False: the last WAL upload failed and none has succeeded since.
type ArchivingFailure struct {
	Namespace string
	Cluster   string
	// Since is when the condition turned False; zero when the cluster's time
	// cannot be read.
	Since   time.Time
	Message string
}

// ArchivingFailures picks the clusters that are failing to archive. A
// hibernated cluster is skipped: it runs nothing, so nothing is left unarchived
// (EXC-758 leaves a paused project's last backup valid).
func ArchivingFailures(clusters []*unstructured.Unstructured) []ArchivingFailure {
	var failures []ArchivingFailure
	for _, cluster := range clusters {
		if cluster == nil || cluster.GetAnnotations()["cnpg.io/hibernation"] == "on" {
			continue
		}
		conditions, _, _ := unstructured.NestedSlice(cluster.Object, "status", "conditions")
		for _, entry := range conditions {
			condition, ok := entry.(map[string]interface{})
			if !ok || condition["type"] != "ContinuousArchiving" || condition["status"] != "False" {
				continue
			}
			failure := ArchivingFailure{Namespace: cluster.GetNamespace(), Cluster: cluster.GetName()}
			failure.Message, _ = condition["message"].(string)
			if stamp, _ := condition["lastTransitionTime"].(string); stamp != "" {
				if parsed, err := time.Parse(time.RFC3339, stamp); err == nil {
					failure.Since = parsed
				}
			}
			failures = append(failures, failure)
		}
	}
	return failures
}
