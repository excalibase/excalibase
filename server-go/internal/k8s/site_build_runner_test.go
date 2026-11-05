package k8s

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

func newBuildRunner(t *testing.T) (*SiteBuildRunner, *Client) {
	t.Helper()
	client := newFakeClient(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "site-builds"}})
	return NewSiteBuildRunner(client, testBuildOptions()), client
}

func TestSiteBuildRunner_StartPreparesTheNamespaceAndResumesOnceSecretsExist(t *testing.T) {
	runner, client := newBuildRunner(t)
	ctx := context.Background()
	spec := testRunSpec()
	if err := runner.Start(ctx, spec); err != nil {
		t.Fatalf("start: %v", err)
	}
	name := siteBuildName(spec.BuildID)
	job, err := client.clientset.BatchV1().Jobs("site-builds").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("job: %v", err)
	}
	if job.Spec.Suspend == nil || *job.Spec.Suspend {
		t.Error("the job must be resumed once its Secrets exist")
	}
	for _, secretName := range []string{name + "-env", name + "-git"} {
		secret, err := client.clientset.CoreV1().Secrets("site-builds").Get(ctx, secretName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("secret %s: %v", secretName, err)
		}
		owners := secret.OwnerReferences
		if len(owners) != 1 || owners[0].Kind != "Job" || owners[0].Name != name || owners[0].UID != job.UID {
			t.Errorf("%s must be owned by its Job so it goes with it: %+v", secretName, owners)
		}
	}
	if _, err := client.clientset.NetworkingV1().NetworkPolicies("site-builds").Get(ctx, siteBuildEgressName, metav1.GetOptions{}); err != nil {
		t.Errorf("egress policy: %v", err)
	}
	if _, err := client.clientset.CoreV1().ResourceQuotas("site-builds").Get(ctx, siteBuildQuotaName, metav1.GetOptions{}); err != nil {
		t.Errorf("quota: %v", err)
	}
	scripts, err := client.clientset.CoreV1().ConfigMaps("site-builds").Get(ctx, siteBuildScriptsName, metav1.GetOptions{})
	if err != nil || !strings.Contains(scripts.Data["fence.sh"], "KUBERNETES_SERVICE_HOST") {
		t.Errorf("scripts: %v", err)
	}
}

func TestSiteBuildRunner_StartConvergesAnEditedPolicy(t *testing.T) {
	runner, client := newBuildRunner(t)
	ctx := context.Background()
	if err := runner.Start(ctx, testRunSpec()); err != nil {
		t.Fatal(err)
	}
	policies := client.clientset.NetworkingV1().NetworkPolicies("site-builds")
	edited, _ := policies.Get(ctx, siteBuildEgressName, metav1.GetOptions{})
	edited.Spec.Egress = nil
	if _, err := policies.Update(ctx, edited, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	second := testRunSpec()
	second.BuildID = "11111111-2222-3333-4444-555555555555"
	if err := runner.Start(ctx, second); err != nil {
		t.Fatal(err)
	}
	restored, _ := policies.Get(ctx, siteBuildEgressName, metav1.GetOptions{})
	if len(restored.Spec.Egress) != 2 {
		t.Errorf("a loosened or emptied fence must be put back: %+v", restored.Spec.Egress)
	}
}

func TestSiteBuildRunner_StartRefusesAMissingNamespace(t *testing.T) {
	runner := NewSiteBuildRunner(newFakeClient(), testBuildOptions())
	if err := runner.Start(context.Background(), testRunSpec()); err == nil {
		t.Fatal("a missing build namespace must fail the start, not be created on the fly")
	}
}

func TestSiteBuildRunner_StatusReadsTheJobAndItsPod(t *testing.T) {
	runner, client := newBuildRunner(t)
	ctx := context.Background()
	spec := testRunSpec()
	if err := runner.Start(ctx, spec); err != nil {
		t.Fatal(err)
	}
	status, err := runner.Status(ctx, spec.BuildID)
	if err != nil || status.Phase != sitebuilds.RunPending {
		t.Fatalf("no pod yet: %+v %v", status, err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "site-build-x-abcde", Namespace: "site-builds", Labels: map[string]string{siteBuildComponentKey: siteBuildComponent, siteBuildLabelBuild: spec.BuildID}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, InitContainerStatuses: passedInits(),
			ContainerStatuses: []corev1.ContainerStatus{running("collect", true)}},
	}
	if _, err := client.clientset.CoreV1().Pods("site-builds").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	status, err = runner.Status(ctx, spec.BuildID)
	if err != nil || status.Phase != sitebuilds.RunReady {
		t.Fatalf("ready: %+v %v", status, err)
	}
}

func TestSiteBuildRunner_StatusOfAVanishedJobIsAFailure(t *testing.T) {
	runner, _ := newBuildRunner(t)
	status, err := runner.Status(context.Background(), "gone")
	if err != nil || status.Phase != sitebuilds.RunFailed || status.Failure != sitebuilds.FailPlatform {
		t.Fatalf("got %+v %v", status, err)
	}
}

func TestSiteBuildRunner_CollectStreamsFromTheCollectStep(t *testing.T) {
	runner, client := newBuildRunner(t)
	ctx := context.Background()
	spec := testRunSpec()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "site-builds", Labels: map[string]string{siteBuildComponentKey: siteBuildComponent, siteBuildLabelBuild: spec.BuildID}}}
	if _, err := client.clientset.CoreV1().Pods("site-builds").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	var gotPod, gotContainer string
	var gotCommand []string
	runner.stream = func(_ context.Context, _, podName, container string, command []string, out io.Writer) error {
		gotPod, gotContainer, gotCommand = podName, container, command
		_, err := out.Write([]byte("archive-bytes"))
		return err
	}
	var buffer bytes.Buffer
	if err := runner.Collect(ctx, spec.BuildID, &buffer, 1024); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if buffer.String() != "archive-bytes" || gotPod != "p1" || gotContainer != "collect" || strings.Join(gotCommand, " ") != "cat "+siteBuildArchivePath {
		t.Errorf("collected %q from %s/%s with %v", buffer.String(), gotPod, gotContainer, gotCommand)
	}
	buffer.Reset()
	if err := runner.Collect(ctx, spec.BuildID, &buffer, 4); !errors.Is(err, sitebuilds.ErrOutputTooLarge) {
		t.Errorf("past the limit: err = %v", err)
	}
}

func TestSiteBuildRunner_RemoveDeletesTheJobAndItsSecrets(t *testing.T) {
	runner, client := newBuildRunner(t)
	ctx := context.Background()
	spec := testRunSpec()
	if err := runner.Start(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if err := runner.Remove(ctx, spec.BuildID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	name := siteBuildName(spec.BuildID)
	if _, err := client.clientset.BatchV1().Jobs("site-builds").Get(ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("job still there: %v", err)
	}
	if _, err := client.clientset.CoreV1().Secrets("site-builds").Get(ctx, name+"-git", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("git secret still there: %v", err)
	}
	if err := runner.Remove(ctx, spec.BuildID); err != nil {
		t.Errorf("removing twice: %v", err)
	}
}

func TestSiteBuildRunner_LogsKeepEachStepsTail(t *testing.T) {
	runner, client := newBuildRunner(t)
	ctx := context.Background()
	spec := testRunSpec()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "site-builds", Labels: map[string]string{siteBuildComponentKey: siteBuildComponent, siteBuildLabelBuild: spec.BuildID}},
		Status:     corev1.PodStatus{InitContainerStatuses: passedInits()},
	}
	if _, err := client.clientset.CoreV1().Pods("site-builds").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	logs, err := runner.Logs(ctx, spec.BuildID)
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	for _, header := range []string{"== clone ==", "== build ==", "== network fence =="} {
		if !strings.Contains(logs, header) {
			t.Errorf("logs lack %q:\n%s", header, logs)
		}
	}
	if strings.Contains(logs, "== collect ==") {
		t.Error("a step that never started has no log section")
	}
}

func TestKeepTail(t *testing.T) {
	if got := keepTail("abcdef", 3); got != "…def" {
		t.Errorf("got %q", got)
	}
	if got := keepTail("abc", 3); got != "abc" {
		t.Errorf("got %q", got)
	}
}

// A build printing far more than the kept tail never makes the controller hold more than twice it.
func TestTailWriterStaysBounded(t *testing.T) {
	tail := &tailWriter{limit: 10}
	for range 1000 {
		if _, err := tail.Write([]byte("0123456789abcdef")); err != nil {
			t.Fatal(err)
		}
		if len(tail.data) > 20 {
			t.Fatalf("held %d bytes", len(tail.data))
		}
	}
	if got := tail.String(); got != "…6789abcdef" {
		t.Errorf("tail %q", got)
	}
}

// A malformed size, say from an old row, fails that build instead of panicking the controller.
func TestSiteBuildRunner_StartRefusesAMalformedSize(t *testing.T) {
	for _, change := range []func(*sitebuilds.RunSpec){
		func(spec *sitebuilds.RunSpec) { spec.Memory = "lots" },
		func(spec *sitebuilds.RunSpec) { spec.Disk = "" },
		func(spec *sitebuilds.RunSpec) { spec.LargestPlan.Memory = "4 gigs" },
	} {
		runner, _ := newBuildRunner(t)
		spec := testRunSpec()
		change(&spec)
		if err := runner.Start(context.Background(), spec); err == nil {
			t.Errorf("started %+v", spec)
		}
	}
}
