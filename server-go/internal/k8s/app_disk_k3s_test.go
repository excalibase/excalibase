//go:build live

package k8s

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

// k3s ships local-path as its default StorageClass: the storage the platform runs on.
const liveDiskStorageClass = "local-path"

// Run with: go test ./internal/k8s/ -tags=live -run TestK3sAppDiskUnderGVisor -v -count=1 -timeout 30m
func TestK3sAppDiskUnderGVisor(t *testing.T) {
	g := startGVisorK3s(t, gvisorPlatformSystrap)
	namespace := g.createNamespace(t, "org1-proj-disk")
	// A non-root image: its uid has to be able to write to the volume.
	app := liveApp("keeper", "nginxinc/nginx-unprivileged:1.27", 8080)
	app.Disk = &apphost.AppDisk{MountPath: "/data", Size: "1Gi"}
	deploy := func(deployID string) {
		t.Helper()
		if err := g.deployDiskApp(t, namespace, app, deployID); err != nil {
			t.Fatalf("deploy %s: %v", deployID, err)
		}
	}
	deploy("dep-1")
	pod := g.appPod(t, namespace, app.Name)
	t.Run("the pod runs under gVisor", func(t *testing.T) { assertGVisorSignature(t, g, pod) })
	g.exec(t, pod, "echo kept-across-restarts > /data/proof.txt && sync")
	g.assertProof(t, namespace, app, "first write")

	t.Run("survives a pod restart", func(t *testing.T) {
		if err := g.clientset.CoreV1().Pods(namespace).Delete(context.Background(), pod.Name, metav1.DeleteOptions{}); err != nil {
			t.Fatalf("delete pod: %v", err)
		}
		g.waitForReplacement(t, namespace, app, pod.Name)
		g.assertProof(t, namespace, app, "after a restart")
	})
	t.Run("survives a redeploy", func(t *testing.T) {
		app.Env = []apphost.EnvVar{{Name: "REVISION", Kind: apphost.KindLiteral, Value: stringPointer("2")}}
		deploy("dep-2")
		g.assertProof(t, namespace, app, "after a redeploy")
	})
	t.Run("survives a pause and a resume", func(t *testing.T) {
		ctx := context.Background()
		if err := g.client.PauseAppWorkload(ctx, namespace, app.ID); err != nil {
			t.Fatalf("pause: %v", err)
		}
		if err := g.client.WaitForAppPodsGone(ctx, namespace, app.ID, 2*time.Minute); err != nil {
			t.Fatalf("pods after pause: %v", err)
		}
		if err := g.client.ResumeAppWorkload(ctx, namespace, app.ID, app.Name, app.Tier, 4*time.Minute); err != nil {
			t.Fatalf("resume: %v", err)
		}
		g.assertProof(t, namespace, app, "after a pause and resume")
	})
	t.Run("survives a rename", func(t *testing.T) {
		ctx := context.Background()
		app.Name = "keeper-renamed"
		if err := g.client.PruneAppWorkload(ctx, namespace, app.ID, app.Name, 2*time.Minute); err != nil {
			t.Fatalf("stop the old name: %v", err)
		}
		deploy("dep-3")
		g.assertProof(t, namespace, app, "after a rename")
	})
	t.Run("a grow on local-path is refused, the disk untouched", func(t *testing.T) {
		class, err := g.clientset.StorageV1().StorageClasses().Get(context.Background(), liveDiskStorageClass, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("read storage class: %v", err)
		}
		if class.AllowVolumeExpansion != nil && *class.AllowVolumeExpansion {
			t.Fatalf("fixture: %s allows expansion here; the refusal cannot be exercised", liveDiskStorageClass)
		}
		if err := g.client.GrowAppDisk(context.Background(), namespace, app.ID, "2Gi"); !errors.Is(err, ErrAppDiskNotExpandable) {
			t.Fatalf("grow: got %v, want ErrAppDiskNotExpandable", err)
		}
		g.assertProof(t, namespace, app, "after a refused grow")
	})
	t.Run("deleting the app deletes the disk", func(t *testing.T) {
		volume := g.boundVolume(t, namespace, app)
		if err := g.client.DeleteAppWorkload(context.Background(), namespace, app.ID, 3*time.Minute); err != nil {
			t.Fatalf("delete: %v", err)
		}
		g.assertNothingOwned(t, namespace, app.ID)
		claims, err := g.clientset.CoreV1().PersistentVolumeClaims(namespace).List(context.Background(), metav1.ListOptions{})
		if err != nil || len(claims.Items) != 0 {
			t.Fatalf("claims left: %v %v", claims, err)
		}
		g.waitForVolumeGone(t, volume)
	})
}

func stringPointer(value string) *string { return &value }

func (g *gvisorCluster) deployDiskApp(t *testing.T, namespace string, app *apphost.App, deployID string) error {
	t.Helper()
	workload, err := RenderAppWorkload(namespace, app, nil, AppRenderOptions{
		RuntimeClass: gvisorRuntimeClass, Route: liveRoute, DeployID: deployID, DiskStorageClass: liveDiskStorageClass})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	ctx := context.Background()
	if err := g.client.ApplyAppWorkload(ctx, namespace, workload); err != nil {
		t.Fatalf("apply: %v", err)
	}
	return g.client.WaitForAppRollout(ctx, namespace, AppObjectName(app.Name), deployID, 5*time.Minute)
}

// assertProof reads the file back from whichever pod now serves the app.
func (g *gvisorCluster) assertProof(t *testing.T, namespace string, app *apphost.App, when string) {
	t.Helper()
	pod := g.appPodByID(t, namespace, app.ID)
	got := strings.TrimSpace(g.exec(t, pod, "cat /data/proof.txt"))
	if got != "kept-across-restarts" {
		t.Fatalf("%s: /data/proof.txt in %s = %q", when, pod.Name, got)
	}
	t.Logf("%s: pod %s reads %q from its disk", when, pod.Name, got)
}

func (g *gvisorCluster) waitForReplacement(t *testing.T, namespace string, app *apphost.App, old string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		pods, err := g.clientset.CoreV1().Pods(namespace).List(context.Background(),
			metav1.ListOptions{LabelSelector: "excalibase.io/app=" + app.ID})
		if err == nil && len(pods.Items) == 1 && pods.Items[0].Name != old && podReady(pods.Items[0]) {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatal("the restarted pod never became ready")
}

// appPodByID finds the app by its id, which a rename keeps; its only pod once any older one has gone.
func (g *gvisorCluster) appPodByID(t *testing.T, namespace, appID string) corev1.Pod {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		pods, err := g.clientset.CoreV1().Pods(namespace).List(context.Background(),
			metav1.ListOptions{LabelSelector: "excalibase.io/app=" + appID})
		if err == nil && len(pods.Items) == 1 && podReady(pods.Items[0]) {
			return pods.Items[0]
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("app %s never had exactly one ready pod", appID)
	return corev1.Pod{}
}

func podReady(pod corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (g *gvisorCluster) boundVolume(t *testing.T, namespace string, app *apphost.App) string {
	t.Helper()
	claim, err := g.clientset.CoreV1().PersistentVolumeClaims(namespace).Get(context.Background(), AppDiskClaimName(app.ID), metav1.GetOptions{})
	if err != nil || claim.Spec.VolumeName == "" {
		t.Fatalf("the disk is not bound: %v", err)
	}
	return claim.Spec.VolumeName
}

// waitForVolumeGone: local-path reclaims with Delete, so the data itself goes with the claim.
func (g *gvisorCluster) waitForVolumeGone(t *testing.T, volume string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := g.clientset.CoreV1().PersistentVolumes().Get(context.Background(), volume, metav1.GetOptions{}); err != nil {
			t.Logf("volume %s is gone: %v", volume, err)
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("volume %s outlived its claim", volume)
}
