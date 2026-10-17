package k8s

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// mockMOCOBackupImage is the image MOCO v0.37.0 runs its jobs with.
const mockMOCOBackupImage = "ghcr.io/cybozu-go/moco-backup:0.37.0"

// MockMOCOBackupCronJob is the CronJob MOCO v0.37.0 renders for a cluster's
// BackupPolicy (controllers/mysqlcluster_controller.go reconcileV1BackupJob),
// reduced to what the platform reads. An empty endpoint leaves the flag out.
func MockMOCOBackupCronJob(namespace, cluster, image, endpoint string) *unstructured.Unstructured {
	args := []interface{}{"backup", "--threads=1"}
	if endpoint != "" {
		args = append(args, "--endpoint="+endpoint)
	}
	args = append(args, "--use-path-style", "--backend-type=s3", "backups", namespace, cluster)
	labels := map[string]interface{}{
		"app.kubernetes.io/created-by": "moco",
		"app.kubernetes.io/instance":   cluster,
		"app.kubernetes.io/name":       "mysql-backup",
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "batch/v1",
		"kind":       "CronJob",
		"metadata":   map[string]interface{}{"name": mocoBackupJobPrefix + cluster, "namespace": namespace, "labels": labels},
		"spec": map[string]interface{}{
			"schedule": "0 * * * *",
			"jobTemplate": map[string]interface{}{
				"metadata": map[string]interface{}{"labels": labels},
				"spec": map[string]interface{}{"template": map[string]interface{}{
					"metadata": map[string]interface{}{"labels": labels},
					"spec": map[string]interface{}{
						"restartPolicy":      "Never",
						"serviceAccountName": MySQLBackupServiceAccount,
						"containers": []interface{}{map[string]interface{}{
							"name": mocoBackupContainerName, "image": image, "args": args,
						}},
					},
				}},
			},
		},
	}}
}

// reconcileMOCOBackupJob does what MOCO does with a cluster's backupPolicyName:
// it makes the backup CronJob once the policy exists and removes it when the
// cluster is unlinked. Callers hold m.mu.
func (m *MockClient) reconcileMOCOBackupJob(namespace string, cluster *unstructured.Unstructured) {
	key := namespace + "/" + mocoBackupJobPrefix + cluster.GetName()
	policyName, linked, _ := unstructured.NestedString(cluster.Object, "spec", "backupPolicyName")
	if !linked {
		delete(m.CRDs, key)
		return
	}
	policy, ok := m.CRDs[namespace+"/"+policyName]
	if _, exists := m.CRDs[key]; exists || !ok {
		return
	}
	endpoint, _, _ := unstructured.NestedString(policy.Object, "spec", "jobConfig", "bucketConfig", "endpointURL")
	m.CRDs[key] = MockMOCOBackupCronJob(namespace, cluster.GetName(), mockMOCOBackupImage, endpoint)
}
