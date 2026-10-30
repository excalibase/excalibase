package k8s

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/excalibase/provisioning-poc/internal/appbuilds"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

func newAppBuildRunner(t *testing.T) (*AppBuildRunner, *Client) {
	t.Helper()
	client := newFakeClient(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app-builds"}},
		&nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "gvisor"}, Handler: "runsc"})
	return NewAppBuildRunner(client, testAppBuildOptions()), client
}

// The Dockerfile step is looser than a site build's, so a RuntimeClass that is not gVisor refuses every build.
func TestAppBuildRunner_StartRefusesARuntimeOutsideGVisor(t *testing.T) {
	client := newFakeClient(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app-builds"}},
		&nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "gvisor"}, Handler: "runc"})
	err := NewAppBuildRunner(client, testAppBuildOptions()).Start(context.Background(), testAppRunSpec(appbuilds.BuilderDockerfile))
	if err == nil || !strings.Contains(err.Error(), "not gVisor") {
		t.Fatalf("started under runc: %v", err)
	}
	missing := NewAppBuildRunner(newFakeClient(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app-builds"}}), testAppBuildOptions())
	if err := missing.Start(context.Background(), testAppRunSpec(appbuilds.BuilderDockerfile)); err == nil {
		t.Fatal("started with no RuntimeClass")
	}
}

// A start that fails after the Job exists removes it, so no suspended Job holds a slot.
func TestAppBuildRunner_AFailedStartLeavesNoJob(t *testing.T) {
	runner, client := newAppBuildRunner(t)
	fakeset := client.clientset.(*fake.Clientset)
	fakeset.PrependReactor("create", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("secrets are down")
	})
	spec := testAppRunSpec(appbuilds.BuilderDockerfile)
	if err := runner.Start(context.Background(), spec); err == nil {
		t.Fatal("started without its secrets")
	}
	if _, err := client.clientset.BatchV1().Jobs("app-builds").Get(context.Background(), appBuildName(spec.BuildID), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("the suspended job is still there: %v", err)
	}
}

func TestAppBuildRunner_StartPreparesTheNamespaceAndResumesOnceSecretsExist(t *testing.T) {
	runner, client := newAppBuildRunner(t)
	ctx := context.Background()
	spec := testAppRunSpec(appbuilds.BuilderDockerfile)
	if err := runner.Start(ctx, spec); err != nil {
		t.Fatalf("start: %v", err)
	}
	name := appBuildName(spec.BuildID)
	job, err := client.clientset.BatchV1().Jobs("app-builds").Get(ctx, name, metav1.GetOptions{})
	if err != nil || *job.Spec.Suspend {
		t.Fatalf("job resumed: %v", err)
	}
	for _, secretName := range []string{name + "-secrets", name + "-git"} {
		secret, err := client.clientset.CoreV1().Secrets("app-builds").Get(ctx, secretName, metav1.GetOptions{})
		if err != nil || len(secret.OwnerReferences) != 1 || secret.OwnerReferences[0].UID != job.UID {
			t.Errorf("%s owned by its job: %v", secretName, err)
		}
	}
	if _, err := client.clientset.NetworkingV1().NetworkPolicies("app-builds").Get(ctx, appBuildEgressName, metav1.GetOptions{}); err != nil {
		t.Errorf("egress policy: %v", err)
	}
	if _, err := client.clientset.CoreV1().ResourceQuotas("app-builds").Get(ctx, appBuildQuotaName, metav1.GetOptions{}); err != nil {
		t.Errorf("quota: %v", err)
	}
	scripts, err := client.clientset.CoreV1().ConfigMaps("app-builds").Get(ctx, appBuildScriptsName, metav1.GetOptions{})
	if err != nil || !strings.Contains(scripts.Data["fence.sh"], "KUBERNETES_SERVICE_HOST") || !strings.Contains(scripts.Data["collect.sh"], "image.tar") ||
		!strings.Contains(scripts.Data["clone.sh"], "GIT_CONFIG_VALUE_0") {
		t.Errorf("scripts: %v", err)
	}
}

func TestAppBuildRunner_StartRefusesAnInvalidSpecOrMissingNamespace(t *testing.T) {
	runner, _ := newAppBuildRunner(t)
	spec := testAppRunSpec(appbuilds.BuilderDockerfile)
	spec.RootDir = "../../etc"
	if err := runner.Start(context.Background(), spec); err == nil {
		t.Error("a root dir outside the checkout must be refused")
	}
	bare := NewAppBuildRunner(newFakeClient(&nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "gvisor"}, Handler: "runsc"}), testAppBuildOptions())
	if err := bare.Start(context.Background(), testAppRunSpec(appbuilds.BuilderBuildpacks)); err == nil {
		t.Error("a missing namespace must fail the start, not be created on the fly")
	}
}

func TestAppBuildRunner_StatusCollectLogsAndRemove(t *testing.T) {
	runner, client := newAppBuildRunner(t)
	ctx := context.Background()
	spec := testAppRunSpec(appbuilds.BuilderDockerfile)
	if status, err := runner.Status(ctx, spec.BuildID); err != nil || status.Failure != sitebuilds.FailPlatform {
		t.Fatalf("a build with no job: %+v %v", status, err)
	}
	if err := runner.Start(ctx, spec); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "app-builds", Labels: map[string]string{siteBuildComponentKey: appBuildComponent, siteBuildLabelBuild: spec.BuildID}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, InitContainerStatuses: []corev1.ContainerStatus{
			terminated("prepare-workdir", "Completed", 0), terminated("wait-for-fence", "Completed", 0),
			terminated("clone", "Completed", 0), terminated("buildkit", "Completed", 0)},
			ContainerStatuses: []corev1.ContainerStatus{running("collect", true)}},
	}
	if _, err := client.clientset.CoreV1().Pods("app-builds").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if status, err := runner.Status(ctx, spec.BuildID); err != nil || status.Phase != sitebuilds.RunReady {
		t.Fatalf("ready: %+v %v", status, err)
	}
	var command []string
	runner.stream = func(_ context.Context, _, _, container string, cmd []string, out io.Writer) error {
		command = append([]string{container}, cmd...)
		_, err := out.Write([]byte("oci-archive"))
		return err
	}
	var archive bytes.Buffer
	if err := runner.Collect(ctx, spec.BuildID, &archive, 1024); err != nil || archive.String() != "oci-archive" ||
		strings.Join(command, " ") != "collect cat "+appBuildArchivePath {
		t.Errorf("collect %q via %v: %v", archive.String(), command, err)
	}
	if err := runner.Collect(ctx, spec.BuildID, &archive, 3); !errors.Is(err, sitebuilds.ErrOutputTooLarge) {
		t.Errorf("an image past the plan's cap: %v", err)
	}
	logs, err := runner.Logs(ctx, spec.BuildID)
	if err != nil || !strings.Contains(logs, "== docker build ==") || strings.Contains(logs, "== buildpacks ==") {
		t.Errorf("logs %q: %v", logs, err)
	}
	if err := runner.Remove(ctx, spec.BuildID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.clientset.CoreV1().Secrets("app-builds").Get(ctx, appBuildName(spec.BuildID)+"-secrets", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("secrets left behind: %v", err)
	}
}

// Each step's failure is named in the user's terms.
func TestAppBuildStatus_NamesTheFailingStep(t *testing.T) {
	cases := []struct {
		step string
		code int32
		want sitebuilds.FailureKind
	}{
		{"wait-for-fence", 1, sitebuilds.FailFence},
		{"clone", 1, sitebuilds.FailClone},
		{"buildkit", 12, appbuilds.FailNoDockerfile},
		{"buildkit", 13, sitebuilds.FailRootDir},
		{"analyze", 13, sitebuilds.FailRootDir},
		{"buildkit", 1, sitebuilds.FailBuild},
		{"buildpacks", 20, appbuilds.FailNoBuildpack},
		{"buildpacks", 51, sitebuilds.FailBuild},
		{"analyze", 1, sitebuilds.FailPlatform},
		{"export", 1, sitebuilds.FailBuild},
		{"run-image", 1, sitebuilds.FailPlatform},
		{"collect", 3, sitebuilds.FailNoOutput},
	}
	for _, tc := range cases {
		pod := buildPod(corev1.PodPending, []corev1.ContainerStatus{terminated(tc.step, "Error", tc.code)}, nil)
		status := interpretBuild(&batchv1.Job{}, pod, appStepFailureKind)
		if status.Phase != sitebuilds.RunFailed || status.Failure != tc.want {
			t.Errorf("%s exit %d: %+v, want %s", tc.step, tc.code, status, tc.want)
		}
	}
	oom := buildPod(corev1.PodRunning, []corev1.ContainerStatus{terminated("buildpacks", "OOMKilled", 0)}, nil)
	if status := interpretBuild(&batchv1.Job{}, oom, appStepFailureKind); status.Failure != sitebuilds.FailOutOfMemory {
		t.Errorf("an OOM kill whatever its exit code: %+v", status)
	}
}
