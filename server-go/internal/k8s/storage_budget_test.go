package k8s

import (
	"context"
	"errors"
	"github.com/excalibase/provisioning-poc/internal/apphost"
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func budgetFakeClient(objects ...runtime.Object) (*Client, *fake.Clientset, *dynamicfake.FakeDynamicClient) {
	clientset := fake.NewSimpleClientset(objects...)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			CNPGClusterGVR: "ClusterList",
			LVMNodeGVR:     "LVMNodeList",
		})
	return NewClientFromInterfaces(clientset, dyn), clientset, dyn
}

func budgetClaim(namespace, name, request, capacity string, labels map[string]string) *corev1.PersistentVolumeClaim {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec: corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(request)},
		}},
	}
	if capacity != "" {
		pvc.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(capacity)}
	}
	return pvc
}

func projectNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"excalibase.io/type": "project"}}}
}

func cnpgCluster(namespace, name string, instances int64, size string) *unstructured.Unstructured {
	cluster := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster",
		"metadata": map[string]interface{}{"name": name, "namespace": namespace},
		"spec":     map[string]interface{}{"instances": instances, "storage": map[string]interface{}{"size": size}},
	}}
	return cluster
}

// Every claim counts at what it reserves (the larger of its request and its
// capacity), and a cluster whose claims are not all made yet counts at what
// it will make, so an allocation in flight is never free space.
func TestStorageAllocated_CountsClaimsAndClustersInFlight(t *testing.T) {
	c, _, dyn := budgetFakeClient(
		projectNamespace("org-p1"), projectNamespace("org-p2"),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "excalibase-platform"}},
		// p1: a 3-instance 5Gi cluster with two claims so far, and an app disk.
		budgetClaim("org-p1", "p1-postgres-1", "5Gi", "5Gi", map[string]string{"cnpg.io/cluster": "p1-postgres"}),
		budgetClaim("org-p1", "p1-postgres-2", "5Gi", "", map[string]string{"cnpg.io/cluster": "p1-postgres"}),
		budgetClaim("org-p1", "app-disk-a1", "1Gi", "1Gi", nil),
		// p2: a grown claim whose capacity is above its request.
		budgetClaim("org-p2", "p2-postgres-1", "5Gi", "6Gi", map[string]string{"cnpg.io/cluster": "p2-postgres"}),
		// The platform's own volumes count too.
		budgetClaim("excalibase-platform", "nats-data", "5Gi", "5Gi", nil),
	)
	ctx := context.Background()
	for _, cluster := range []*unstructured.Unstructured{cnpgCluster("org-p1", "p1-postgres", 3, "5Gi"), cnpgCluster("org-p2", "p2-postgres", 1, "5Gi")} {
		if _, err := dyn.Resource(CNPGClusterGVR).Namespace(cluster.GetNamespace()).Create(ctx, cluster, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := c.StorageAllocated(ctx)
	if err != nil {
		t.Fatalf("StorageAllocated: %v", err)
	}
	gi := int64(1) << 30
	want := StorageAllocation{TenantBytes: 17 * gi, PlatformBytes: 5 * gi, PendingBytes: 5 * gi}
	if got != want {
		t.Fatalf("allocation = %+v, want %+v", got, want)
	}
	if got.Total() != 27*gi {
		t.Fatalf("total = %d", got.Total())
	}
}

func lvmNode(name string, groups ...map[string]interface{}) *unstructured.Unstructured {
	list := make([]interface{}, 0, len(groups))
	for _, group := range groups {
		list = append(list, group)
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "local.openebs.io/v1alpha1", "kind": "LVMNode",
		"metadata":     map[string]interface{}{"name": name, "namespace": "openebs"},
		"volumeGroups": list,
	}}
}

func TestLVMVolumeGroupBytes_SumsTheGroupOnEveryNode(t *testing.T) {
	c, _, dyn := budgetFakeClient()
	ctx := context.Background()
	for _, node := range []*unstructured.Unstructured{
		lvmNode("n1", map[string]interface{}{"name": "excalibase-tenants", "size": "61436Mi"}, map[string]interface{}{"name": "other", "size": "1Ti"}),
		lvmNode("n2", map[string]interface{}{"name": "excalibase-tenants", "size": "100Gi"}),
	} {
		if _, err := dyn.Resource(LVMNodeGVR).Namespace("openebs").Create(ctx, node, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := c.LVMVolumeGroupBytes(ctx, "openebs", "excalibase-tenants")
	if err != nil || got != 61436<<20+100<<30 {
		t.Fatalf("got %d, %v", got, err)
	}
	if _, err := c.LVMVolumeGroupBytes(ctx, "openebs", "missing"); !errors.Is(err, ErrStorageCapacityUnknown) {
		t.Fatalf("a missing group: err = %v, want ErrStorageCapacityUnknown", err)
	}
}

func TestRequireSizedStorageClass(t *testing.T) {
	yes, no := true, false
	c, _, _ := budgetFakeClient(
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "excalibase-tenant"}, Provisioner: "local.csi.openebs.io", AllowVolumeExpansion: &yes},
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "local-path"}, Provisioner: "rancher.io/local-path"},
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "fixed-lvm"}, Provisioner: "local.csi.openebs.io", AllowVolumeExpansion: &no},
	)
	sized := []string{"local.csi.openebs.io"}
	ctx := context.Background()
	if err := c.RequireSizedStorageClass(ctx, "excalibase-tenant", sized); err != nil {
		t.Fatalf("the LVM class refused: %v", err)
	}
	for _, name := range []string{"local-path", "fixed-lvm", "missing"} {
		if err := c.RequireSizedStorageClass(ctx, name, sized); !errors.Is(err, ErrStorageNotSized) {
			t.Errorf("%s: err = %v, want ErrStorageNotSized", name, err)
		}
	}
}

// recordingReserver admits up to room bytes and records every reservation.
type recordingReserver struct {
	room  int64
	asked []int64
}

var errNoRoom = errors.New("no room")

func (r *recordingReserver) Reserve(_ context.Context, add int64, _ string, allocate func() error) error {
	r.asked = append(r.asked, add)
	if add > r.room {
		return errNoRoom
	}
	r.room -= add
	return allocate()
}

// Creating a database cluster reserves its instances' volumes; one the budget
// cannot hold is never created.
func TestApplyCRDReservesADatabaseClustersVolumes(t *testing.T) {
	c, _, dyn := budgetFakeClient()
	reserver := &recordingReserver{room: 10 * (1 << 30)}
	c.SetStorageReserver(reserver)
	ctx := context.Background()
	if err := c.ApplyCRD(ctx, CNPGClusterGVR, "org-p1", cnpgCluster("org-p1", "p1-postgres", 3, "5Gi")); !errors.Is(err, errNoRoom) {
		t.Fatalf("err = %v, want the reservation refused", err)
	}
	if _, err := dyn.Resource(CNPGClusterGVR).Namespace("org-p1").Get(ctx, "p1-postgres", metav1.GetOptions{}); err == nil {
		t.Fatal("a refused cluster was created")
	}
	if err := c.ApplyCRD(ctx, CNPGClusterGVR, "org-p1", cnpgCluster("org-p1", "p1-postgres", 1, "5Gi")); err != nil {
		t.Fatalf("a 5Gi cluster within the room: %v", err)
	}
	// Applying it again asks only for what it grows by.
	if err := c.ApplyCRD(ctx, CNPGClusterGVR, "org-p1", cnpgCluster("org-p1", "p1-postgres", 1, "6Gi")); err != nil {
		t.Fatal(err)
	}
	gi := int64(1) << 30
	if len(reserver.asked) != 3 || reserver.asked[0] != 15*gi || reserver.asked[1] != 5*gi || reserver.asked[2] != gi {
		t.Fatalf("asked = %v", reserver.asked)
	}
}

func TestAppDiskClaimsAreReserved(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	yes := true
	if _, err := clientset.StorageV1().StorageClasses().Create(context.Background(),
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "lvm"}, Provisioner: "local.csi.openebs.io", AllowVolumeExpansion: &yes}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	reserver := &recordingReserver{room: 6 << 30}
	c.SetStorageReserver(reserver)
	app := diskApp() // 5Gi
	opts := testRenderOptions
	opts.DiskStorageClass = "lvm"
	if err := c.ApplyAppWorkload(context.Background(), testNamespace, renderWithOptions(t, app, opts)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := c.GrowAppDisk(context.Background(), testNamespace, app.ID, 0, "8Gi"); !errors.Is(err, errNoRoom) {
		t.Fatalf("grow past the room: err = %v", err)
	}
	if err := c.GrowAppDisk(context.Background(), testNamespace, app.ID, 0, "6Gi"); err != nil {
		t.Fatalf("grow within the room: %v", err)
	}
	gi := int64(1) << 30
	if len(reserver.asked) != 3 || reserver.asked[0] != 5*gi || reserver.asked[1] != 3*gi || reserver.asked[2] != gi {
		t.Fatalf("asked = %v, want the new claim then each growth", reserver.asked)
	}
}

// Moving a disk onto a smaller volume frees storage overall, so it is admitted
// even with no room left: only growth beyond the old volume is reserved.
func TestCopyAppDiskReservesOnlyGrowth(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	app := diskApp() // 5Gi
	deployedApp(t, c, app)
	runJobsAs(clientset, 0, "")
	reserver := &recordingReserver{room: 0}
	c.SetStorageReserver(reserver)
	if err := c.CopyAppDisk(context.Background(), testNamespace, app, apphost.AppDisk{MountPath: "/data", Size: "1Gi", Generation: 1}, "", testDiskJobs); err != nil {
		t.Fatalf("a smaller copy with no room left: %v", err)
	}
	if len(reserver.asked) != 1 || reserver.asked[0] > 0 {
		t.Fatalf("asked = %v, want nothing more than the old volume", reserver.asked)
	}
}
