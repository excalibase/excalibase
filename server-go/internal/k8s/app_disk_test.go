package k8s

import (
	"context"
	"errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

func diskApp() *apphost.App {
	app := minimalApp()
	app.ID = "app-01h"
	app.Disk = &apphost.AppDisk{MountPath: "/data", Size: "5Gi"}
	return app
}

func renderWithOptions(t *testing.T, app *apphost.App, opts AppRenderOptions) *AppWorkload {
	t.Helper()
	workload, err := RenderAppWorkload(testNamespace, app, newResolver(), opts)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return workload
}

func TestRenderAppWorkloadGivesADiskItsOwnClaim(t *testing.T) {
	opts := testRenderOptions
	opts.DiskStorageClass = "local-path"
	claim := renderWithOptions(t, diskApp(), opts).Disk
	if claim == nil {
		t.Fatal("an app with a disk must render a claim")
	}
	if claim.Name != AppDiskClaimName("app-01h", 0) || claim.Namespace != testNamespace {
		t.Errorf("claim %s/%s, want %s/%s", claim.Namespace, claim.Name, testNamespace, AppDiskClaimName("app-01h", 0))
	}
	if len(claim.Spec.AccessModes) != 1 || claim.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Errorf("access modes = %v, want ReadWriteOnce only", claim.Spec.AccessModes)
	}
	size := claim.Spec.Resources.Requests[corev1.ResourceStorage]
	if size.Cmp(resource.MustParse("5Gi")) != 0 {
		t.Errorf("requested %s, want 5Gi", size.String())
	}
	if claim.Spec.StorageClassName == nil || *claim.Spec.StorageClassName != "local-path" {
		t.Errorf("storage class = %v, want local-path", claim.Spec.StorageClassName)
	}
	for key, want := range map[string]string{
		"excalibase.io/app": "app-01h", "excalibase.io/project": "proj-abc",
		"app.kubernetes.io/managed-by": appManagedByValue,
	} {
		if claim.Labels[key] != want {
			t.Errorf("label %s = %q, want %q", key, claim.Labels[key], want)
		}
	}
	// Named by id and unlabelled by name, so a rename neither orphans nor prunes it.
	if _, named := claim.Labels["app.kubernetes.io/name"]; named {
		t.Error("the claim must not carry the app's name")
	}
	if len(claim.OwnerReferences) != 0 {
		t.Error("the claim must outlive the Deployment, so nothing may own it")
	}
}

// An empty class is the platform's default: the cluster's default StorageClass, as a database's.
func TestRenderAppWorkloadDiskOnTheDefaultStorageClass(t *testing.T) {
	claim := renderWithOptions(t, diskApp(), testRenderOptions).Disk
	if claim.Spec.StorageClassName != nil {
		t.Errorf("storage class = %q, want the cluster default (unset)", *claim.Spec.StorageClassName)
	}
}

func TestRenderAppWorkloadMountsTheDiskAndRecreates(t *testing.T) {
	workload := renderWithOptions(t, diskApp(), testRenderOptions)
	spec := workload.Deployment.Spec
	if spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType || spec.Strategy.RollingUpdate != nil {
		t.Errorf("strategy = %+v, want Recreate: two pods must never hold one disk", spec.Strategy)
	}
	pod := spec.Template.Spec
	if len(pod.Volumes) != 1 || pod.Volumes[0].PersistentVolumeClaim == nil ||
		pod.Volumes[0].PersistentVolumeClaim.ClaimName != workload.Disk.Name {
		t.Fatalf("volumes = %+v, want the app's claim", pod.Volumes)
	}
	mounts := pod.Containers[0].VolumeMounts
	if len(mounts) != 1 || mounts[0].Name != pod.Volumes[0].Name || mounts[0].MountPath != "/data" || mounts[0].ReadOnly {
		t.Fatalf("mounts = %+v, want the disk read-write at /data", mounts)
	}
}

func TestRenderAppWorkloadWithoutADiskRendersNoClaim(t *testing.T) {
	workload := mustRender(t, minimalApp(), newResolver())
	if workload.Disk != nil {
		t.Fatalf("claim = %+v, want none", workload.Disk)
	}
	if workload.Deployment.Spec.Strategy.Type != appsv1.RollingUpdateDeploymentStrategyType {
		t.Errorf("an app without a disk keeps its zero-downtime rollout")
	}
}

func TestRenderAppWorkloadRefusesADiskWithSeveralCopies(t *testing.T) {
	app := diskApp()
	app.Tier = fullApp().Tier
	app.Replicas = 2
	if _, err := RenderAppWorkload(testNamespace, app, newResolver(), testRenderOptions); !errors.Is(err, ErrRenderApp) {
		t.Fatalf("err = %v, want a render refusal", err)
	}
}

func TestApplyAppWorkload_CreatesTheDiskBeforeTheDeployment(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	ctx := context.Background()
	app := diskApp()
	deployedApp(t, c, app)
	claim, err := clientset.CoreV1().PersistentVolumeClaims(testNamespace).Get(ctx, AppDiskClaimName(app.ID, 0), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the disk must exist: %v", err)
	}
	var order []string
	for _, action := range clientset.Actions() {
		if action.GetVerb() == "create" {
			order = append(order, action.GetResource().Resource)
		}
	}
	if indexOf(order, "persistentvolumeclaims") > indexOf(order, "deployments") {
		t.Errorf("create order = %v, the claim must come before the Deployment", order)
	}
	if got := claim.Spec.Resources.Requests[corev1.ResourceStorage]; got.String() != "5Gi" {
		t.Errorf("size = %s, want 5Gi", got.String())
	}
}

// A redeploy never touches the disk: growing is its own call, and shrinking is never possible.
func TestApplyAppWorkload_LeavesAnExistingDiskAsItIs(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	ctx := context.Background()
	app := diskApp()
	deployedApp(t, c, app)
	claims := clientset.CoreV1().PersistentVolumeClaims(testNamespace)
	grown, _ := claims.Get(ctx, AppDiskClaimName(app.ID, 0), metav1.GetOptions{})
	grown.Spec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("9Gi")
	if _, err := claims.Update(ctx, grown, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	deployedApp(t, c, app)
	after, _ := claims.Get(ctx, AppDiskClaimName(app.ID, 0), metav1.GetOptions{})
	if got := after.Spec.Resources.Requests[corev1.ResourceStorage]; got.String() != "9Gi" {
		t.Errorf("a redeploy changed the disk to %s", got.String())
	}
}

func TestPruneAppWorkload_KeepsTheDisk(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	app := diskApp()
	deployedApp(t, c, app)
	if err := c.PruneAppWorkload(context.Background(), testNamespace, app.ID, "renamed", shortWait); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if _, err := clientset.CoreV1().PersistentVolumeClaims(testNamespace).Get(context.Background(), AppDiskClaimName(app.ID, 0), metav1.GetOptions{}); err != nil {
		t.Fatalf("a rename must keep the disk: %v", err)
	}
}

func TestDeleteAppWorkload_DeletesTheDiskAfterThePods(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	ctx := context.Background()
	app := diskApp()
	deployedApp(t, c, app)
	if _, err := clientset.CoreV1().Pods(testNamespace).Create(ctx, appPodObject(app, "web-1"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	claims := clientset.CoreV1().PersistentVolumeClaims(testNamespace)
	if err := c.DeleteAppWorkload(ctx, testNamespace, app.ID, shortWait); !errors.Is(err, ErrAppPodsRemain) {
		t.Fatalf("err = %v, want ErrAppPodsRemain", err)
	}
	if _, err := claims.Get(ctx, AppDiskClaimName(app.ID, 0), metav1.GetOptions{}); err != nil {
		t.Fatalf("the disk must stay while a pod may still write to it: %v", err)
	}
	if err := clientset.CoreV1().Pods(testNamespace).Delete(ctx, "web-1", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteAppWorkload(ctx, testNamespace, app.ID, shortWait); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if _, err := claims.Get(ctx, AppDiskClaimName(app.ID, 0), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the disk must be deleted with the app, got %v", err)
	}
}

func TestDeleteAppWorkload_LeavesAnotherAppsDisk(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	app := diskApp()
	other := diskApp()
	other.ID, other.Name = "app-other", "other"
	deployedApp(t, c, app)
	deployedApp(t, c, other)
	if err := c.DeleteAppWorkload(context.Background(), testNamespace, app.ID, shortWait); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := clientset.CoreV1().PersistentVolumeClaims(testNamespace).Get(context.Background(), AppDiskClaimName(other.ID, 0), metav1.GetOptions{}); err != nil {
		t.Fatalf("another app's disk must stay: %v", err)
	}
}

func expandableClass(name string, expandable bool) *storagev1.StorageClass {
	return &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: name}, Provisioner: "test", AllowVolumeExpansion: &expandable}
}

func deployedDiskOn(t *testing.T, class string, expandable bool) (*Client, *apphost.App) {
	t.Helper()
	c, clientset := newLifecycleFakeClient()
	if _, err := clientset.StorageV1().StorageClasses().Create(context.Background(), expandableClass(class, expandable), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	app := diskApp()
	opts := testRenderOptions
	opts.DiskStorageClass = class
	if err := c.ApplyAppWorkload(context.Background(), testNamespace, renderWithOptions(t, app, opts)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	return c, app
}

func TestGrowAppDisk_AsksTheClaimForTheNewSize(t *testing.T) {
	c, app := deployedDiskOn(t, "expandable", true)
	if err := c.GrowAppDisk(context.Background(), testNamespace, app.ID, 0, "8Gi"); err != nil {
		t.Fatalf("grow: %v", err)
	}
	claim, _ := c.clientset.CoreV1().PersistentVolumeClaims(testNamespace).Get(context.Background(), AppDiskClaimName(app.ID, 0), metav1.GetOptions{})
	if got := claim.Spec.Resources.Requests[corev1.ResourceStorage]; got.String() != "8Gi" {
		t.Fatalf("requested %s, want 8Gi", got.String())
	}
}

func TestGrowAppDisk_RefusesAClassThatCannotExpand(t *testing.T) {
	c, app := deployedDiskOn(t, "fixed", false)
	err := c.GrowAppDisk(context.Background(), testNamespace, app.ID, 0, "8Gi")
	if !errors.Is(err, ErrAppDiskNotExpandable) {
		t.Fatalf("err = %v, want ErrAppDiskNotExpandable", err)
	}
	claim, _ := c.clientset.CoreV1().PersistentVolumeClaims(testNamespace).Get(context.Background(), AppDiskClaimName(app.ID, 0), metav1.GetOptions{})
	if got := claim.Spec.Resources.Requests[corev1.ResourceStorage]; got.String() != "5Gi" {
		t.Fatalf("a refused grow changed the claim to %s", got.String())
	}
}

func TestGrowAppDisk_ADiskOnTheDefaultClassIsNotAssumedExpandable(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	app := diskApp()
	deployedApp(t, c, app)
	claim, _ := clientset.CoreV1().PersistentVolumeClaims(testNamespace).Get(context.Background(), AppDiskClaimName(app.ID, 0), metav1.GetOptions{})
	if claim.Spec.StorageClassName != nil {
		t.Fatal("fixture: the claim should name no class")
	}
	if err := c.GrowAppDisk(context.Background(), testNamespace, app.ID, 0, "8Gi"); !errors.Is(err, ErrAppDiskNotExpandable) {
		t.Fatalf("err = %v, want ErrAppDiskNotExpandable", err)
	}
}

func TestGrowAppDisk_NoClaimYet(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	if err := c.GrowAppDisk(context.Background(), testNamespace, "app-01h", 0, "8Gi"); !errors.Is(err, ErrAppDiskNotCreated) {
		t.Fatalf("err = %v, want ErrAppDiskNotCreated", err)
	}
}

// Only a DNS name can name a claim; an id that is not one is refused, never rewritten.
func TestRenderAppWorkloadRefusesADiskItCannotName(t *testing.T) {
	app := diskApp()
	app.ID = "App_01"
	if _, err := RenderAppWorkload(testNamespace, app, newResolver(), testRenderOptions); !errors.Is(err, ErrRenderApp) {
		t.Fatalf("err = %v, want a render refusal", err)
	}
}

func indexOf(items []string, want string) int {
	for i, item := range items {
		if item == want {
			return i
		}
	}
	return len(items)
}

// Without custom domains provisioning has no right to Certificates, so it
// never made one: a refused list is nothing to prune, not a failed deploy.
func TestPruneAppWorkload_WithoutRightsToCertificatesPrunesTheRest(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	app := diskApp()
	deployedApp(t, c, app)
	dyn := c.dynamicClient.(*dynamicfake.FakeDynamicClient)
	dyn.PrependReactor("list", "certificates", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "cert-manager.io", Resource: "certificates"}, "", errors.New("no rule"))
	})
	if err := c.PruneAppWorkload(context.Background(), testNamespace, app.ID, "renamed", shortWait); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if err := c.DeleteAppWorkload(context.Background(), testNamespace, app.ID, shortWait); err != nil {
		t.Fatalf("delete: %v", err)
	}
}
