package k8s

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func freeCluster(t *testing.T) *unstructured.Unstructured {
	t.Helper()
	opts := clusterOpts("p1", "org-p1")
	opts.Tier = config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5", StatementTimeout: "15s"}
	opts.StorageClass = "fast-ssd"
	opts.Parameters = map[string]string{"work_mem": "4MB", "jit": "off"}
	return BuildPostgreSQLCluster(opts)
}

func specString(t *testing.T, cluster *unstructured.Unstructured, fields ...string) string {
	t.Helper()
	value, _, err := unstructured.NestedString(cluster.Object, append([]string{"spec"}, fields...)...)
	if err != nil {
		t.Fatalf("read %v: %v", fields, err)
	}
	return value
}

func TestClusterStorageSizeReadsTheVolumeTheClusterAsksFor(t *testing.T) {
	size, err := ClusterStorageSize(freeCluster(t))
	if err != nil {
		t.Fatalf("ClusterStorageSize: %v", err)
	}
	if size.Cmp(resource.MustParse("5Gi")) != 0 {
		t.Errorf("size = %s, want 5Gi", size.String())
	}
}

func TestClusterStorageSizeRefusesAClusterWithNoSize(t *testing.T) {
	cluster := freeCluster(t)
	unstructured.RemoveNestedField(cluster.Object, "spec", "storage", "size")
	if _, err := ClusterStorageSize(cluster); err == nil {
		t.Fatal("a cluster with no storage size was read as having one")
	}
}

func TestWithStorageSizeChangesOnlyTheSizeOfACopy(t *testing.T) {
	cluster := freeCluster(t)
	grown := WithStorageSize(cluster, "8Gi")
	if got := specString(t, grown, "storage", "size"); got != "8Gi" {
		t.Errorf("size = %q, want 8Gi", got)
	}
	if got := specString(t, grown, "storage", "storageClass"); got != "fast-ssd" {
		t.Errorf("storage class = %q, want it kept", got)
	}
	if got := specString(t, cluster, "storage", "size"); got != "5Gi" {
		t.Errorf("the original cluster was changed to %q", got)
	}
}

func TestWithTenantParametersReplacesOnlyTheTenantsSettings(t *testing.T) {
	cluster := freeCluster(t)
	tuned := WithTenantParameters(cluster, map[string]string{"work_mem": "8MB", "shared_preload_libraries": "evil"})

	params := TenantParameters(tuned)
	if len(params) != 1 || params["work_mem"] != "8MB" {
		t.Errorf("tenant parameters = %v, want only work_mem=8MB (jit dropped, platform-only name ignored)", params)
	}
	all, _, _ := unstructured.NestedStringMap(tuned.Object, "spec", "postgresql", "parameters")
	if all["statement_timeout"] != "15s" || all["max_connections"] != "100" {
		t.Errorf("platform settings were lost: %v", all)
	}
	if _, set := all["shared_preload_libraries"]; set {
		t.Error("a platform-only parameter reached the cluster")
	}
	if TenantParameters(cluster)["jit"] != "off" {
		t.Error("the original cluster was changed")
	}
}

func TestWithTierResizesAndSpreadsACopyAndKeepsTheStorageClass(t *testing.T) {
	cluster := freeCluster(t)
	standard := config.TierConfig{Instances: 3, StorageSize: "50Gi", Memory: "4Gi", CPU: "2", StatementTimeout: "30s"}
	moved := WithTier(cluster, standard)

	instances, _, _ := unstructured.NestedInt64(moved.Object, "spec", "instances")
	if instances != 3 {
		t.Errorf("instances = %d, want 3", instances)
	}
	if got := specString(t, moved, "storage", "size"); got != "50Gi" {
		t.Errorf("storage = %q, want 50Gi", got)
	}
	if got := specString(t, moved, "storage", "storageClass"); got != "fast-ssd" {
		t.Errorf("storage class = %q, want the cluster's own", got)
	}
	if got := specString(t, moved, "resources", "limits", "memory"); got != "4Gi" {
		t.Errorf("memory limit = %q, want 4Gi", got)
	}
	if got := specString(t, moved, "affinity", "podAntiAffinityType"); got != "required" {
		t.Errorf("anti-affinity = %q, want required", got)
	}
	params, _, _ := unstructured.NestedStringMap(moved.Object, "spec", "postgresql", "parameters")
	if params["statement_timeout"] != "30s" || params["idle_in_transaction_session_timeout"] != "30s" {
		t.Errorf("statement guard not moved to the new tier: %v", params)
	}
	if params["work_mem"] != "4MB" {
		t.Errorf("tenant parameters were lost: %v", params)
	}
	if specString(t, cluster, "storage", "size") != "5Gi" {
		t.Error("the original cluster was changed")
	}
}

func TestWithTierToOneInstanceDropsTheSpreadRule(t *testing.T) {
	opts := clusterOpts("p1", "org-p1")
	spread := BuildPostgreSQLCluster(opts)
	free := config.TierConfig{Instances: 1, StorageSize: "500Gi", Memory: "512Mi", CPU: "0.5", StatementTimeout: "15s"}
	moved := WithTier(spread, free)
	if _, found, _ := unstructured.NestedMap(moved.Object, "spec", "affinity"); found {
		t.Error("a single-instance cluster kept the one-per-node rule")
	}
}

func claim(name, namespace, cluster, class string) *corev1.PersistentVolumeClaim {
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: namespace, Labels: map[string]string{"cnpg.io/cluster": cluster},
	}}
	if class != "" {
		pvc.Spec.StorageClassName = &class
	}
	return pvc
}

func storageClass(name string, expandable *bool) *storagev1.StorageClass {
	return &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: name}, AllowVolumeExpansion: expandable}
}

func TestClusterVolumesExpandable(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name    string
		objects []runtime.Object
		refused bool
	}{
		{"every claim on an expandable class", []runtime.Object{
			storageClass("csi", &yes), claim("p1-postgres-1", "ns", "p1-postgres", "csi"), claim("p1-postgres-2", "ns", "p1-postgres", "csi"),
		}, false},
		{"a class that does not allow expansion", []runtime.Object{
			storageClass("local-path", &no), claim("p1-postgres-1", "ns", "p1-postgres", "local-path"),
		}, true},
		{"a class that does not say", []runtime.Object{
			storageClass("local-path", nil), claim("p1-postgres-1", "ns", "p1-postgres", "local-path"),
		}, true},
		{"a claim with no class", []runtime.Object{claim("p1-postgres-1", "ns", "p1-postgres", "")}, true},
		{"a class that is gone", []runtime.Object{claim("p1-postgres-1", "ns", "p1-postgres", "missing")}, true},
		{"no claims at all", nil, true},
		{"only another cluster's claims", []runtime.Object{
			storageClass("csi", &yes), claim("other-postgres-1", "ns", "other-postgres", "csi"),
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newFakeClient(tc.objects...)
			err := client.ClusterVolumesExpandable(context.Background(), "ns", "p1-postgres")
			if tc.refused && !errors.Is(err, ErrVolumeExpansionUnsupported) {
				t.Fatalf("err = %v, want ErrVolumeExpansionUnsupported", err)
			}
			if !tc.refused && err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
		})
	}
}

// Putting a failed tier change back restores the size, spread and settings,
// but not the disk: a volume that grew cannot be asked to shrink.
func TestWithSizingOfRestoresTheSizeButKeepsTheDisk(t *testing.T) {
	before := freeCluster(t)
	moved := WithTier(before, config.TierConfig{Instances: 3, StorageSize: "50Gi", Memory: "4Gi", CPU: "2", StatementTimeout: "30s"})
	back := WithSizingOf(moved, before)

	if got := specString(t, back, "storage", "size"); got != "50Gi" {
		t.Errorf("storage = %q, want the grown 50Gi kept", got)
	}
	instances, _, _ := unstructured.NestedInt64(back.Object, "spec", "instances")
	if instances != 1 || specString(t, back, "resources", "limits", "memory") != "512Mi" ||
		specString(t, back, "postgresql", "parameters", "statement_timeout") != "15s" ||
		specString(t, back, "primaryUpdateMethod") != "restart" {
		t.Errorf("size not put back: %v", back.Object["spec"])
	}
	if _, found, _ := unstructured.NestedMap(back.Object, "spec", "affinity"); found {
		t.Error("the one-per-node rule of the failed plan was kept")
	}
}

func TestValidateTierSizingRefusesAnIncompleteTier(t *testing.T) {
	if err := ValidateTierSizing(config.TierConfig{Instances: 1, StorageSize: "5Gi", CPU: "1"}); !errors.Is(err, ErrTierSizingIncomplete) {
		t.Errorf("err = %v, want ErrTierSizingIncomplete", err)
	}
	if err := ValidateTierSizing(config.TierConfig{Instances: 1, StorageSize: "5Gi", CPU: "1", Memory: "1Gi"}); err != nil {
		t.Errorf("a complete tier was refused: %v", err)
	}
}

func TestClusterStorageSizeRefusesASizeThatIsNotAQuantity(t *testing.T) {
	if _, err := ClusterStorageSize(WithStorageSize(freeCluster(t), "lots")); err == nil {
		t.Fatal("a size that is not a quantity was read")
	}
}
