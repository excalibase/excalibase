package k8s

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

const shortWait = 50 * time.Millisecond

func newLifecycleFakeClient() (*Client, *fake.Clientset) {
	clientset := fake.NewSimpleClientset()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{CiliumNetworkPolicyGVR: "CiliumNetworkPolicyList"})
	return NewClientFromInterfaces(clientset, dyn), clientset
}

// convergeOnUpdate plays the Deployment controller: every replica the spec asks for is at once available.
func convergeOnUpdate(clientset *fake.Clientset) {
	clientset.PrependReactor("update", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		dep := action.(k8stesting.UpdateAction).GetObject().(*appsv1.Deployment)
		want := *dep.Spec.Replicas
		dep.Status = appsv1.DeploymentStatus{Replicas: want, UpdatedReplicas: want, AvailableReplicas: want}
		return false, nil, nil
	})
}

func deployedApp(t *testing.T, c *Client, app *apphost.App) {
	t.Helper()
	if err := c.ApplyAppWorkload(context.Background(), testNamespace, mustRender(t, app, newResolver())); err != nil {
		t.Fatalf("apply: %v", err)
	}
}

func appPodObject(app *apphost.App, name string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: testNamespace, Labels: appLabels(app),
	}}
}

func readDeployment(t *testing.T, c *Client, app *apphost.App) *appsv1.Deployment {
	t.Helper()
	dep, err := c.clientset.AppsV1().Deployments(testNamespace).Get(context.Background(), AppObjectName(app.Name), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read deployment: %v", err)
	}
	return dep
}

func TestPauseAppWorkload_ScalesToZeroAndRemembersTheReplicas(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	app := fullApp()
	deployedApp(t, c, app)

	if err := c.PauseAppWorkload(context.Background(), testNamespace, app.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	dep := readDeployment(t, c, app)
	if *dep.Spec.Replicas != 0 {
		t.Errorf("replicas = %d, want 0", *dep.Spec.Replicas)
	}
	if dep.Annotations[appPausedReplicasAnnotation] != "3" {
		t.Errorf("paused replicas annotation = %q, want 3", dep.Annotations[appPausedReplicasAnnotation])
	}
	if dep.Spec.Template.Spec.Containers[0].Image != app.Image {
		t.Error("pause must keep the rest of the workload as it is")
	}

	if err := c.PauseAppWorkload(context.Background(), testNamespace, app.ID); err != nil {
		t.Fatalf("second pause: %v", err)
	}
	if got := readDeployment(t, c, app).Annotations[appPausedReplicasAnnotation]; got != "3" {
		t.Errorf("a repeated pause must keep the count to resume to, got %q", got)
	}
}

func TestPauseAppWorkload_NothingDeployed(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	err := c.PauseAppWorkload(context.Background(), testNamespace, "app-missing")
	if !errors.Is(err, ErrAppNotDeployed) {
		t.Fatalf("err = %v, want ErrAppNotDeployed", err)
	}
}

func TestResumeAppWorkload_RestoresTheReplicasAndWaitsForThem(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	convergeOnUpdate(clientset)
	app := fullApp()
	deployedApp(t, c, app)
	if err := c.PauseAppWorkload(context.Background(), testNamespace, app.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}

	if err := c.ResumeAppWorkload(context.Background(), testNamespace, app.ID, app.Name, time.Second); err != nil {
		t.Fatalf("resume: %v", err)
	}
	dep := readDeployment(t, c, app)
	if *dep.Spec.Replicas != 3 {
		t.Errorf("replicas = %d, want 3", *dep.Spec.Replicas)
	}
	if _, still := dep.Annotations[appPausedReplicasAnnotation]; still {
		t.Error("a finished resume must drop the paused marker")
	}
}

func TestResumeAppWorkload_NotReadyKeepsThePausedMarker(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	app := fullApp()
	deployedApp(t, c, app)
	if err := c.PauseAppWorkload(context.Background(), testNamespace, app.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}

	err := c.ResumeAppWorkload(context.Background(), testNamespace, app.ID, app.Name, shortWait)
	if !errors.Is(err, ErrAppRollout) {
		t.Fatalf("err = %v, want ErrAppRollout", err)
	}
	if got := readDeployment(t, c, app).Annotations[appPausedReplicasAnnotation]; got != "3" {
		t.Errorf("a failed resume must stay retryable, marker = %q", got)
	}
}

func TestResumeAppWorkload_RefusesWhatWasNotPaused(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	app := fullApp()
	deployedApp(t, c, app)

	err := c.ResumeAppWorkload(context.Background(), testNamespace, app.ID, app.Name, shortWait)
	if !errors.Is(err, ErrAppNotPaused) {
		t.Fatalf("err = %v, want ErrAppNotPaused", err)
	}
	if err := c.ResumeAppWorkload(context.Background(), testNamespace, "app-missing", "missing", shortWait); !errors.Is(err, ErrAppNotDeployed) {
		t.Fatalf("missing deployment: err = %v, want ErrAppNotDeployed", err)
	}
}

func TestResumeAppWorkload_RefusesACorruptMarker(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	app := fullApp()
	deployedApp(t, c, app)
	dep := readDeployment(t, c, app)
	dep.Annotations[appPausedReplicasAnnotation] = "lots"
	if _, err := c.clientset.AppsV1().Deployments(testNamespace).Update(context.Background(), dep, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := c.ResumeAppWorkload(context.Background(), testNamespace, app.ID, app.Name, shortWait); err == nil {
		t.Fatal("want a refusal for an unreadable replica count")
	}
}

func TestApplyAppWorkload_ADeployClearsThePausedMarker(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	app := fullApp()
	deployedApp(t, c, app)
	if err := c.PauseAppWorkload(context.Background(), testNamespace, app.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	deployedApp(t, c, app)
	dep := readDeployment(t, c, app)
	if _, still := dep.Annotations[appPausedReplicasAnnotation]; still {
		t.Error("a deploy decides the replicas; the paused marker must go")
	}
	if *dep.Spec.Replicas != 3 {
		t.Errorf("replicas = %d, want 3", *dep.Spec.Replicas)
	}
}

func TestWaitForAppPodsGone_ATerminatingPodStillCounts(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	app := fullApp()
	pod := appPodObject(app, "web-1")
	now := metav1.Now()
	pod.DeletionTimestamp = &now
	pod.Finalizers = []string{"example.com/hold"}
	if _, err := clientset.CoreV1().Pods(testNamespace).Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed pod: %v", err)
	}

	err := c.WaitForAppPodsGone(context.Background(), testNamespace, app.ID, shortWait)
	if !errors.Is(err, ErrAppPodsRemain) || !strings.Contains(err.Error(), "web-1") {
		t.Fatalf("err = %v, want ErrAppPodsRemain naming web-1", err)
	}

	if err := clientset.CoreV1().Pods(testNamespace).Delete(context.Background(), "web-1", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("remove pod: %v", err)
	}
	if err := c.WaitForAppPodsGone(context.Background(), testNamespace, app.ID, shortWait); err != nil {
		t.Fatalf("no pods left: %v", err)
	}
}

func TestWaitForAppPodsGone_IgnoresOtherApps(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	other := fullApp()
	other.ID = "app-other"
	if _, err := clientset.CoreV1().Pods(testNamespace).Create(context.Background(), appPodObject(other, "other-1"), metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := c.WaitForAppPodsGone(context.Background(), testNamespace, fullApp().ID, shortWait); err != nil {
		t.Fatalf("another app's pod must not hold this one: %v", err)
	}
}

func TestDeleteAppWorkload_KeepsTheFenceUntilThePodsAreGone(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	app := fullApp()
	deployedApp(t, c, app)
	ctx := context.Background()
	if _, err := clientset.CoreV1().Pods(testNamespace).Create(ctx, appPodObject(app, "web-1"), metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed pod: %v", err)
	}

	err := c.DeleteAppWorkload(ctx, testNamespace, app.ID, shortWait)
	if !errors.Is(err, ErrAppPodsRemain) {
		t.Fatalf("err = %v, want ErrAppPodsRemain", err)
	}
	name := AppObjectName(app.Name)
	if _, err := clientset.NetworkingV1().Ingresses(testNamespace).Get(ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("the route must go first, got %v", err)
	}
	if _, err := clientset.AppsV1().Deployments(testNamespace).Get(ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("the deployment must be deleted, got %v", err)
	}
	for _, policy := range []string{AppEgressPolicyName(app.Name), AppIngressPolicyName(app.Name)} {
		if _, err := c.GetCRD(ctx, CiliumNetworkPolicyGVR, testNamespace, policy); err != nil {
			t.Errorf("policy %s must stay while a pod runs: %v", policy, err)
		}
	}

	if err := clientset.CoreV1().Pods(testNamespace).Delete(ctx, "web-1", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("remove pod: %v", err)
	}
	if err := c.DeleteAppWorkload(ctx, testNamespace, app.ID, shortWait); err != nil {
		t.Fatalf("retry: %v", err)
	}
	assertNothingLeft(t, c, app)
}

func TestDeleteAppWorkload_LeavesOtherAppsAlone(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	app := fullApp()
	other := fullApp()
	other.ID, other.Name = "app-other", "other"
	other.Env = nil
	deployedApp(t, c, app)
	deployedApp(t, c, other)

	if err := c.DeleteAppWorkload(context.Background(), testNamespace, app.ID, shortWait); err != nil {
		t.Fatalf("delete: %v", err)
	}
	assertNothingLeft(t, c, app)
	readDeployment(t, c, other)
	if _, err := c.GetCRD(context.Background(), CiliumNetworkPolicyGVR, testNamespace, AppEgressPolicyName(other.Name)); err != nil {
		t.Errorf("another app's fence must stay: %v", err)
	}
}

func TestDeleteAppWorkload_NothingDeployedIsDone(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	if err := c.DeleteAppWorkload(context.Background(), testNamespace, fullApp().ID, shortWait); err != nil {
		t.Fatalf("delete of nothing: %v", err)
	}
}

func assertNothingLeft(t *testing.T, c *Client, app *apphost.App) {
	t.Helper()
	ctx := context.Background()
	listOpts := metav1.ListOptions{LabelSelector: appOwnedSelector(app.ID)}
	counts := map[string]int{}
	if list, err := c.clientset.AppsV1().Deployments(testNamespace).List(ctx, listOpts); err == nil {
		counts["deployments"] = len(list.Items)
	}
	if list, err := c.clientset.CoreV1().Services(testNamespace).List(ctx, listOpts); err == nil {
		counts["services"] = len(list.Items)
	}
	if list, err := c.clientset.CoreV1().Secrets(testNamespace).List(ctx, listOpts); err == nil {
		counts["secrets"] = len(list.Items)
	}
	if list, err := c.clientset.NetworkingV1().Ingresses(testNamespace).List(ctx, listOpts); err == nil {
		counts["ingresses"] = len(list.Items)
	}
	if list, err := c.dynamicClient.Resource(CiliumNetworkPolicyGVR).Namespace(testNamespace).List(ctx, listOpts); err == nil {
		counts["policies"] = len(list.Items)
	}
	for kind, n := range counts {
		if n != 0 {
			t.Errorf("%d %s left behind", n, kind)
		}
	}
	if len(counts) != 5 {
		t.Errorf("could not list every kind: %v", counts)
	}
}

func TestDeleteAppWorkload_WaitsForTheReplicaSetsToo(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	app := fullApp()
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "app-web-abc", Namespace: testNamespace, Labels: appLabels(app)}}
	if _, err := clientset.AppsV1().ReplicaSets(testNamespace).Create(context.Background(), rs, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	err := c.DeleteAppWorkload(context.Background(), testNamespace, app.ID, shortWait)
	if !errors.Is(err, ErrAppPodsRemain) || !strings.Contains(err.Error(), "app-web-abc") {
		t.Fatalf("err = %v, want the replica set named", err)
	}
}

func TestPauseAppWorkload_StopsADeploymentLeftUnderAnEarlierName(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	app := fullApp()
	deployedApp(t, c, app)
	renamed := fullApp()
	renamed.Name = "web-renamed"
	deployedApp(t, c, renamed)

	if err := c.PauseAppWorkload(context.Background(), testNamespace, app.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	for _, each := range []*apphost.App{app, renamed} {
		if got := *readDeployment(t, c, each).Spec.Replicas; got != 0 {
			t.Errorf("%s replicas = %d, want 0", each.Name, got)
		}
	}
}

func TestPruneAppWorkload_RemovesOnlyWhatAnEarlierNameLeft(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	ctx := context.Background()
	old := fullApp()
	deployedApp(t, c, old)
	renamed := fullApp()
	renamed.Name = "shop"
	deployedApp(t, c, renamed)
	if _, err := clientset.CoreV1().Pods(testNamespace).Create(ctx, appPodObject(old, "web-old-1"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	err := c.PruneAppWorkload(ctx, testNamespace, old.ID, renamed.Name, shortWait)
	if !errors.Is(err, ErrAppPodsRemain) {
		t.Fatalf("err = %v, want the old pod to hold the prune", err)
	}
	if _, err := clientset.NetworkingV1().Ingresses(testNamespace).Get(ctx, AppObjectName(old.Name), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("the old route must go first, got %v", err)
	}
	if _, err := c.GetCRD(ctx, CiliumNetworkPolicyGVR, testNamespace, AppEgressPolicyName(old.Name)); err != nil {
		t.Errorf("the old fence must stay while its pod runs: %v", err)
	}

	if err := clientset.CoreV1().Pods(testNamespace).Delete(ctx, "web-old-1", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.PruneAppWorkload(ctx, testNamespace, old.ID, renamed.Name, shortWait); err != nil {
		t.Fatalf("prune: %v", err)
	}
	for kind, err := range map[string]error{
		"old deployment": getErr(clientset.AppsV1().Deployments(testNamespace).Get(ctx, AppObjectName(old.Name), metav1.GetOptions{})),
		"old service":    getErr(clientset.CoreV1().Services(testNamespace).Get(ctx, AppObjectName(old.Name), metav1.GetOptions{})),
		"old env secret": getErr(clientset.CoreV1().Secrets(testNamespace).Get(ctx, AppEnvSecretName(old.Name), metav1.GetOptions{})),
	} {
		if !apierrors.IsNotFound(err) {
			t.Errorf("%s left behind: %v", kind, err)
		}
	}
	if _, err := c.GetCRD(ctx, CiliumNetworkPolicyGVR, testNamespace, AppEgressPolicyName(old.Name)); !apierrors.IsNotFound(err) {
		t.Errorf("old fence left behind: %v", err)
	}
	readDeployment(t, c, renamed)
	for _, policy := range []string{AppEgressPolicyName(renamed.Name), AppIngressPolicyName(renamed.Name)} {
		if _, err := c.GetCRD(ctx, CiliumNetworkPolicyGVR, testNamespace, policy); err != nil {
			t.Errorf("the current name's %s must stay: %v", policy, err)
		}
	}
}

func getErr[T any](_ T, err error) error { return err }

func TestResumeAppWorkload_LeavesAnEarlierNameStopped(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	convergeOnUpdate(clientset)
	old := fullApp()
	deployedApp(t, c, old)
	renamed := fullApp()
	renamed.Name = "shop"
	deployedApp(t, c, renamed)
	if err := c.PauseAppWorkload(context.Background(), testNamespace, old.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.ResumeAppWorkload(context.Background(), testNamespace, old.ID, renamed.Name, time.Second); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := *readDeployment(t, c, renamed).Spec.Replicas; got != 3 {
		t.Errorf("current name replicas = %d, want 3", got)
	}
	if got := *readDeployment(t, c, old).Spec.Replicas; got != 0 {
		t.Errorf("the earlier name came back with %d replicas", got)
	}
}
