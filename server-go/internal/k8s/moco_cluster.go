package k8s

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/excalibase/provisioning-poc/internal/config"
)

// MySQL projects run on the MOCO operator (ADR 0041): one MySQLCluster per
// project, named <project>-mysql; MOCO names its pods moco-<cluster>-<n> and
// its Services moco-<cluster>-primary / -replica.
var (
	MOCOClusterGVR = schema.GroupVersionResource{Group: "moco.cybozu.com", Version: "v1beta2", Resource: "mysqlclusters"}
	// ConfigMapGVR and CronJobGVR are read and written through the dynamic
	// client like the operator's own objects.
	ConfigMapGVR = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	CronJobGVR   = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}
)

const (
	mocoAPIVersion     = "moco.cybozu.com/v1beta2"
	mysqlClusterSuffix = "-mysql"
	mysqlDataClaim     = "mysql-data"
	// smallPlanMemory is the request below which a plan runs MySQL lean:
	// a 128MiB buffer pool, a small log buffer and table cache, and no
	// slow-log sidecar (ADR 0041 decision 4).
	smallPlanMemory    = "2Gi"
	smallBufferPool    = "134217728"
	smallLogBuffer     = "16777216"
	smallTableCache    = "1000"
	minTempTableMemory = 32 << 20
	// mysqlMaxConnections replaces MOCO's 100000 with what a tenant gets on
	// Postgres by default.
	mysqlMaxConnections = "100"
	// mocoClusteringStopped is MOCO's switch for its cluster manager.
	mocoClusteringStopped = "moco.cybozu.com/clustering-stopped"
)

// ErrMySQLScaleDown refuses fewer instances: MOCO cannot remove replicas, and
// a Standard → Free move is a restore into a new Free project (ADR 0041).
var ErrMySQLScaleDown = errors.New("a MySQL project cannot run fewer instances; restore it into a new project on the smaller plan instead")

// MySQLClusterOpts is what a project's MySQLCluster is built from.
type MySQLClusterOpts struct {
	ProjectID    string
	Namespace    string
	Tier         config.TierConfig
	StorageClass string
	// Image is the catalogue's digest-pinned image (config.MySQLImage).
	Image string
}

// MySQLClusterName is the project's MySQLCluster.
func MySQLClusterName(projectID string) string { return projectID + mysqlClusterSuffix }

// MySQLConfigName is the ConfigMap of mysqld settings the cluster reads.
func MySQLConfigName(projectID string) string { return MySQLClusterName(projectID) + "-mycnf" }

// MySQLPodName is instance n of the project's cluster.
func MySQLPodName(projectID string, n int64) string {
	return fmt.Sprintf("moco-%s-%d", MySQLClusterName(projectID), n)
}

// MySQLDataClaimPrefix starts the name of every data volume of the
// project's cluster: <claim>-moco-<cluster>-<n>, the StatefulSet's naming.
func MySQLDataClaimPrefix(projectID string) string {
	return mysqlDataClaim + "-moco-" + MySQLClusterName(projectID) + "-"
}

// MySQLPodSelector matches every instance of the project's cluster.
func MySQLPodSelector(projectID string) string {
	return "app.kubernetes.io/name=mysql,app.kubernetes.io/created-by=moco,app.kubernetes.io/instance=" + MySQLClusterName(projectID)
}

// MySQLPrimaryHost is the read-write Service MOCO points at the primary.
func MySQLPrimaryHost(projectID, namespace string) string {
	return fmt.Sprintf("moco-%s-primary.%s.svc.cluster.local", MySQLClusterName(projectID), namespace)
}

// MySQLReplicaHost is the read-only Service over the replicas.
func MySQLReplicaHost(projectID, namespace string) string {
	return fmt.Sprintf("moco-%s-replica.%s.svc.cluster.local", MySQLClusterName(projectID), namespace)
}

func validateMySQLOpts(opts MySQLClusterOpts) error {
	if err := validateTierSizing(opts.Tier); err != nil {
		return err
	}
	if opts.Tier.Instances%2 == 0 {
		return fmt.Errorf("MOCO runs 1, 3 or 5 instances; the plan asks for %d", opts.Tier.Instances)
	}
	if opts.ProjectID == "" || opts.Namespace == "" || opts.Image == "" {
		return errors.New("a MySQL cluster needs its project, namespace and image")
	}
	return nil
}

// BuildMySQLCluster renders the project's MySQLCluster for its plan.
func BuildMySQLCluster(opts MySQLClusterOpts) (*unstructured.Unstructured, error) {
	if err := validateMySQLOpts(opts); err != nil {
		return nil, err
	}
	name := MySQLClusterName(opts.ProjectID)
	cluster := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": mocoAPIVersion,
		"kind":       "MySQLCluster",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": opts.Namespace,
			"labels":    map[string]interface{}{"excalibase.io/project": opts.ProjectID},
		},
		"spec": map[string]interface{}{
			"mysqlConfigMapName": MySQLConfigName(opts.ProjectID),
			"podTemplate": map[string]interface{}{
				"spec": map[string]interface{}{
					"containers": []interface{}{map[string]interface{}{"name": "mysqld", "image": opts.Image}},
				},
			},
		},
	}}
	sized, err := applyMySQLPlan(cluster, opts.Tier, opts.Tier.StorageSize, opts.StorageClass)
	if err != nil {
		return nil, err
	}
	return sized, nil
}

// applyMySQLPlan sets the instance count, resources, disk and sidecars of a
// copy of the cluster.
func applyMySQLPlan(cluster *unstructured.Unstructured, plan config.TierConfig, disk, storageClass string) (*unstructured.Unstructured, error) {
	plan, err := config.MySQLPlan(plan)
	if err != nil {
		return nil, err
	}
	out := cluster.DeepCopy()
	spec := out.Object["spec"].(map[string]interface{})
	small, err := smallMySQLPlan(plan)
	if err != nil {
		return nil, err
	}
	spec["replicas"] = int64(plan.Instances)
	spec["disableSlowQueryLogContainer"] = small
	podSpec := spec["podTemplate"].(map[string]interface{})["spec"].(map[string]interface{})
	mysqld, err := mysqldOf(podSpec)
	if err != nil {
		return nil, err
	}
	mysqld["resources"] = map[string]interface{}{"requests": tierBounds(plan), "limits": tierBounds(plan)}
	if plan.Instances > 1 {
		podSpec["affinity"] = mysqlOneInstancePerNode(out.GetName())
	} else {
		delete(podSpec, "affinity")
	}
	if storageClass == "" {
		storageClass, _ = mysqlStorageClass(out)
	}
	claimSpec := map[string]interface{}{
		"accessModes": []interface{}{"ReadWriteOnce"},
		"resources":   map[string]interface{}{"requests": map[string]interface{}{"storage": disk}},
	}
	if storageClass != "" {
		claimSpec["storageClassName"] = storageClass
	}
	spec["volumeClaimTemplates"] = []interface{}{map[string]interface{}{
		"metadata": map[string]interface{}{"name": mysqlDataClaim},
		"spec":     claimSpec,
	}}
	return out, nil
}

func mysqldOf(podSpec map[string]interface{}) (map[string]interface{}, error) {
	containers, _ := podSpec["containers"].([]interface{})
	for _, raw := range containers {
		if container, ok := raw.(map[string]interface{}); ok && container["name"] == "mysqld" {
			return container, nil
		}
	}
	return nil, errors.New("the MySQL cluster has no mysqld container")
}

func mysqlStorageClass(cluster *unstructured.Unstructured) (string, bool) {
	claims, _, _ := unstructured.NestedSlice(cluster.Object, "spec", "volumeClaimTemplates")
	for _, raw := range claims {
		claim, _ := raw.(map[string]interface{})
		if name, _, _ := unstructured.NestedString(claim, "metadata", "name"); name == mysqlDataClaim {
			class, found, _ := unstructured.NestedString(claim, "spec", "storageClassName")
			return class, found
		}
	}
	return "", false
}

// mysqlOneInstancePerNode keeps a node loss to one instance, like Postgres.
func mysqlOneInstancePerNode(clusterName string) map[string]interface{} {
	return map[string]interface{}{
		"podAntiAffinity": map[string]interface{}{
			"requiredDuringSchedulingIgnoredDuringExecution": []interface{}{map[string]interface{}{
				"topologyKey": "kubernetes.io/hostname",
				"labelSelector": map[string]interface{}{"matchLabels": map[string]interface{}{
					"app.kubernetes.io/name":     "mysql",
					"app.kubernetes.io/instance": clusterName,
				}},
			}},
		},
	}
}

func smallMySQLPlan(plan config.TierConfig) (bool, error) {
	memory, err := resource.ParseQuantity(plan.Memory)
	if err != nil {
		return false, fmt.Errorf("plan memory %q: %w", plan.Memory, err)
	}
	return memory.Cmp(resource.MustParse(smallPlanMemory)) < 0, nil
}

// BuildMySQLConfig renders the mysqld settings the cluster reads: the
// connection cap, the plan's statement budget as max_execution_time, and the
// small buffer pool on a small plan.
func BuildMySQLConfig(opts MySQLClusterOpts) (*unstructured.Unstructured, error) {
	plan, err := config.MySQLPlan(opts.Tier)
	if err != nil {
		return nil, err
	}
	opts.Tier = plan
	data := map[string]interface{}{"max_connections": mysqlMaxConnections}
	small, err := smallMySQLPlan(opts.Tier)
	if err != nil {
		return nil, err
	}
	if small {
		data["innodb_buffer_pool_size"] = smallBufferPool
		data["innodb_log_buffer_size"] = smallLogBuffer
		data["table_open_cache"] = smallTableCache
	}
	memory, err := resource.ParseQuantity(opts.Tier.Memory)
	if err != nil {
		return nil, fmt.Errorf("plan memory %q: %w", opts.Tier.Memory, err)
	}
	// In-memory temp tables default to 1GiB and would OOM a small instance.
	data["temptable_max_ram"] = strconv.FormatInt(max(memory.Value()/16, minTempTableMemory), 10)
	if opts.Tier.StatementTimeout != "" {
		timeout, err := time.ParseDuration(opts.Tier.StatementTimeout)
		if err != nil {
			return nil, fmt.Errorf("plan statement timeout %q: %w", opts.Tier.StatementTimeout, err)
		}
		data["max_execution_time"] = strconv.FormatInt(timeout.Milliseconds(), 10)
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]interface{}{
			"name":      MySQLConfigName(opts.ProjectID),
			"namespace": opts.Namespace,
			"labels":    map[string]interface{}{"excalibase.io/project": opts.ProjectID},
		},
		"data": data,
	}}, nil
}

// MySQLClusterStorageBytes is what the cluster's volumes reserve: instances
// times its disk.
func MySQLClusterStorageBytes(cluster *unstructured.Unstructured) (int64, error) {
	size, err := MySQLClusterDisk(cluster)
	if err != nil {
		return 0, err
	}
	replicas, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "replicas")
	if replicas < 1 {
		replicas = 1
	}
	return replicas * size.Value(), nil
}

// MySQLClusterDisk is the disk each instance asks for.
func MySQLClusterDisk(cluster *unstructured.Unstructured) (resource.Quantity, error) {
	claims, _, _ := unstructured.NestedSlice(cluster.Object, "spec", "volumeClaimTemplates")
	for _, raw := range claims {
		claim, _ := raw.(map[string]interface{})
		if name, _, _ := unstructured.NestedString(claim, "metadata", "name"); name != mysqlDataClaim {
			continue
		}
		size, _, _ := unstructured.NestedString(claim, "spec", "resources", "requests", "storage")
		quantity, err := resource.ParseQuantity(size)
		if err != nil {
			return resource.Quantity{}, fmt.Errorf("cluster %s asks for an unreadable disk %q", cluster.GetName(), size)
		}
		return quantity, nil
	}
	return resource.Quantity{}, fmt.Errorf("cluster %s has no %s claim", cluster.GetName(), mysqlDataClaim)
}

// WithMySQLTier is a copy of the cluster on another plan. Fewer instances
// are refused.
func WithMySQLTier(cluster *unstructured.Unstructured, plan config.TierConfig, disk string) (*unstructured.Unstructured, error) {
	if err := validateTierSizing(plan); err != nil {
		return nil, err
	}
	current, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "replicas")
	if int64(plan.Instances) < current {
		return nil, ErrMySQLScaleDown
	}
	if plan.Instances%2 == 0 {
		return nil, fmt.Errorf("MOCO runs 1, 3 or 5 instances; the plan asks for %d", plan.Instances)
	}
	return applyMySQLPlan(cluster, plan, disk, "")
}

// WithMySQLImage is a copy of the cluster running another image.
func WithMySQLImage(cluster *unstructured.Unstructured, image string) (*unstructured.Unstructured, error) {
	out := cluster.DeepCopy()
	podSpec, _, _ := unstructured.NestedMap(out.Object, "spec", "podTemplate", "spec")
	mysqld, err := mysqldOf(podSpec)
	if err != nil {
		return nil, err
	}
	mysqld["image"] = image
	if err := unstructured.SetNestedMap(out.Object, podSpec, "spec", "podTemplate", "spec"); err != nil {
		return nil, err
	}
	return out, nil
}

// MySQLClusterImage is the image the cluster's mysqld runs.
func MySQLClusterImage(cluster *unstructured.Unstructured) string {
	podSpec, _, _ := unstructured.NestedMap(cluster.Object, "spec", "podTemplate", "spec")
	mysqld, err := mysqldOf(podSpec)
	if err != nil {
		return ""
	}
	image, _ := mysqld["image"].(string)
	return image
}

// WithMySQLOffline is a copy of the cluster with its instances stopped
// (true) or started (false); the volumes stay.
func WithMySQLOffline(cluster *unstructured.Unstructured, offline bool) *unstructured.Unstructured {
	out := cluster.DeepCopy()
	_ = unstructured.SetNestedField(out.Object, offline, "spec", "offline")
	// MOCO 0.34-0.37 panics clustering an offline cluster (cybozu-go/moco#946),
	// so clustering stops with the pause and resumes with the database.
	annotations := out.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	if offline {
		annotations[mocoClusteringStopped] = "true"
	} else {
		delete(annotations, mocoClusteringStopped)
	}
	out.SetAnnotations(annotations)
	return out
}

func mysqlCondition(cluster *unstructured.Unstructured, kind string) bool {
	conditions, _, _ := unstructured.NestedSlice(cluster.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, ok := raw.(map[string]interface{})
		if ok && condition["type"] == kind {
			return condition["status"] == "True"
		}
	}
	return false
}

// MySQLClusterAvailable is MOCO reporting a writable primary.
func MySQLClusterAvailable(cluster *unstructured.Unstructured) bool {
	return mysqlCondition(cluster, "Available")
}

// MySQLClusterRolledOut is MOCO reporting every instance on the current pod
// template; it rolls a change one partition at a time.
func MySQLClusterRolledOut(cluster *unstructured.Unstructured) bool {
	return mysqlCondition(cluster, "StatefulSetReady")
}

// MySQLClusterHealthy is MOCO reporting every instance in sync.
func MySQLClusterHealthy(cluster *unstructured.Unstructured) bool {
	return mysqlCondition(cluster, "Healthy")
}

// MySQLPrimaryPod is the pod MOCO reports as the current primary.
func MySQLPrimaryPod(cluster *unstructured.Unstructured) string {
	index, _, _ := unstructured.NestedInt64(cluster.Object, "status", "currentPrimaryIndex")
	return fmt.Sprintf("moco-%s-%d", cluster.GetName(), index)
}

// MySQLClusterResources is the CPU and memory each mysqld is limited to.
func MySQLClusterResources(cluster *unstructured.Unstructured) (cpu, memory string) {
	podSpec, _, _ := unstructured.NestedMap(cluster.Object, "spec", "podTemplate", "spec")
	mysqld, err := mysqldOf(podSpec)
	if err != nil {
		return "", ""
	}
	limits, _, _ := unstructured.NestedStringMap(mysqld, "resources", "limits")
	return limits["cpu"], limits["memory"]
}
