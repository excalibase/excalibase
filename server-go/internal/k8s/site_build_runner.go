package k8s

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

const (
	// siteBuildLogTailLines and siteBuildLogStepBytes bound what one step keeps in the build history.
	siteBuildLogTailLines = 4000
	siteBuildLogStepBytes = 128 << 10
)

// What a failed apply names.
const (
	siteBuildQuotaKind   = "build quota"
	siteBuildScriptsKind = "build scripts"
)

// siteBuildLogSteps are the steps whose output the history keeps, in order.
var siteBuildLogSteps = []struct{ container, title string }{
	{siteBuildFence, "network fence"}, {siteBuildClone, "clone"}, {siteBuildBuild, "build"}, {siteBuildCollect, "collect"},
}

type streamFunc func(ctx context.Context, namespace, pod, container string, command []string, out io.Writer) error

// SiteBuildRunner runs site builds as Jobs in the build namespace (EXC-354).
type SiteBuildRunner struct {
	client  *Client
	options SiteBuildOptions
	stream  streamFunc
}

var _ sitebuilds.Runner = (*SiteBuildRunner)(nil)

func NewSiteBuildRunner(client *Client, options SiteBuildOptions) *SiteBuildRunner {
	return &SiteBuildRunner{client: client, options: options, stream: client.execStream}
}

// Start puts the fence, quota and scripts in place, creates the Job suspended,
// then its Secrets owned by it, then resumes it.
func (r *SiteBuildRunner) Start(ctx context.Context, spec sitebuilds.RunSpec) error {
	if err := checkSiteBuildSizes(spec); err != nil {
		return err
	}
	if err := r.ensureEnvironment(ctx, spec.LargestPlan); err != nil {
		return err
	}
	jobs := r.client.clientset.BatchV1().Jobs(r.options.Namespace)
	job, err := jobs.Create(ctx, renderSiteBuildJob(r.options, spec), metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create the build job: %w", err)
	}
	owner := metav1.OwnerReference{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: pointerTo(true)}
	env, git := renderSiteBuildSecrets(r.options, spec)
	for _, secret := range []*corev1.Secret{env, git} {
		secret.OwnerReferences = []metav1.OwnerReference{owner}
		if _, err := r.client.clientset.CoreV1().Secrets(r.options.Namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create the build's secret %s: %w", secret.Name, err)
		}
	}
	resume := []byte(`{"spec":{"suspend":false}}`)
	if _, err := jobs.Patch(ctx, job.Name, types.MergePatchType, resume, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("resume the build job: %w", err)
	}
	return nil
}

// checkSiteBuildSizes refuses sizes the renderers would otherwise panic on.
func checkSiteBuildSizes(spec sitebuilds.RunSpec) error {
	for name, value := range map[string]string{"memory": spec.Memory, "disk": spec.Disk,
		"largest plan's memory": spec.LargestPlan.Memory, "largest plan's disk": spec.LargestPlan.Disk} {
		if _, err := resource.ParseQuantity(value); err != nil {
			return fmt.Errorf("the build's %s %q is not a size: %w", name, value, err)
		}
	}
	return nil
}

// ensureEnvironment converges the namespace's shared objects on every start, so an edit cannot loosen them for long.
func (r *SiteBuildRunner) ensureEnvironment(ctx context.Context, largest sitebuilds.BuildSize) error {
	namespace := r.options.Namespace
	if _, err := r.client.clientset.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{}); err != nil {
		return fmt.Errorf("the build namespace %s: %w", namespace, err)
	}
	if err := r.client.applyNetworkPolicy(ctx, renderSiteBuildEgressPolicy(r.options)); err != nil {
		return fmt.Errorf("build egress policy: %w", err)
	}
	if err := r.applyQuota(ctx, renderSiteBuildQuota(r.options, largest)); err != nil {
		return err
	}
	scripts, err := renderSiteBuildScripts(namespace)
	if err != nil {
		return err
	}
	return r.applyConfigMap(ctx, scripts)
}

func (r *SiteBuildRunner) applyQuota(ctx context.Context, desired *corev1.ResourceQuota) error {
	quotas := r.client.clientset.CoreV1().ResourceQuotas(desired.Namespace)
	existing, err := quotas.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = quotas.Create(ctx, desired, metav1.CreateOptions{})
		return wrapApply(siteBuildQuotaKind, err)
	}
	if err != nil {
		return wrapApply(siteBuildQuotaKind, err)
	}
	if equality.Semantic.DeepEqual(existing.Spec, desired.Spec) {
		return nil
	}
	existing.Spec = desired.Spec
	_, err = quotas.Update(ctx, existing, metav1.UpdateOptions{})
	return wrapApply(siteBuildQuotaKind, err)
}

func (r *SiteBuildRunner) applyConfigMap(ctx context.Context, desired *corev1.ConfigMap) error {
	maps := r.client.clientset.CoreV1().ConfigMaps(desired.Namespace)
	existing, err := maps.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = maps.Create(ctx, desired, metav1.CreateOptions{})
		return wrapApply(siteBuildScriptsKind, err)
	}
	if err != nil {
		return wrapApply(siteBuildScriptsKind, err)
	}
	if equality.Semantic.DeepEqual(existing.Data, desired.Data) {
		return nil
	}
	existing.Data = desired.Data
	_, err = maps.Update(ctx, existing, metav1.UpdateOptions{})
	return wrapApply(siteBuildScriptsKind, err)
}

func wrapApply(what string, err error) error {
	if err != nil {
		return fmt.Errorf("apply the %s: %w", what, err)
	}
	return nil
}

func (r *SiteBuildRunner) Status(ctx context.Context, buildID string) (sitebuilds.RunStatus, error) {
	job, err := r.client.clientset.BatchV1().Jobs(r.options.Namespace).Get(ctx, siteBuildName(buildID), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return failed(sitebuilds.FailPlatform, 0, "the build job is gone from the cluster"), nil
	}
	if err != nil {
		return sitebuilds.RunStatus{}, fmt.Errorf("read the build job: %w", err)
	}
	pod, err := r.buildPod(ctx, buildID)
	if err != nil {
		return sitebuilds.RunStatus{}, err
	}
	return interpretSiteBuild(job, pod), nil
}

// buildPod is the build's newest pod, or nil before the Job has made one.
func (r *SiteBuildRunner) buildPod(ctx context.Context, buildID string) (*corev1.Pod, error) {
	pods, err := r.client.clientset.CoreV1().Pods(r.options.Namespace).List(ctx, metav1.ListOptions{LabelSelector: siteBuildLabelBuild + "=" + buildID})
	if err != nil {
		return nil, fmt.Errorf("list the build's pods: %w", err)
	}
	var newest *corev1.Pod
	for i := range pods.Items {
		if newest == nil || pods.Items[i].CreationTimestamp.After(newest.CreationTimestamp.Time) {
			newest = &pods.Items[i]
		}
	}
	return newest, nil
}

// cappedWriter refuses a write past its limit instead of truncating silently.
type cappedWriter struct {
	out        io.Writer
	limit      int64
	written    int64
	overflowed bool
}

func (c *cappedWriter) Write(chunk []byte) (int, error) {
	if c.written+int64(len(chunk)) > c.limit {
		c.overflowed = true
		return 0, sitebuilds.ErrOutputTooLarge
	}
	written, err := c.out.Write(chunk)
	c.written += int64(written)
	return written, err
}

func (r *SiteBuildRunner) Collect(ctx context.Context, buildID string, w io.Writer, limit int64) error {
	pod, err := r.buildPod(ctx, buildID)
	if err != nil {
		return err
	}
	if pod == nil {
		return errors.New("the build has no pod to collect from")
	}
	capped := &cappedWriter{out: w, limit: limit}
	err = r.stream(ctx, r.options.Namespace, pod.Name, siteBuildCollect, []string{"cat", siteBuildArchivePath}, capped)
	if capped.overflowed {
		return sitebuilds.ErrOutputTooLarge
	}
	if err != nil {
		return fmt.Errorf("read the packed output: %w", err)
	}
	return nil
}

// Logs answers each started step's tail, under a header per step.
func (r *SiteBuildRunner) Logs(ctx context.Context, buildID string) (string, error) {
	pod, err := r.buildPod(ctx, buildID)
	if err != nil || pod == nil {
		return "", err
	}
	started := map[string]bool{}
	for _, status := range allStatuses(pod) {
		started[status.Name] = status.State.Running != nil || status.State.Terminated != nil
	}
	var out strings.Builder
	for _, step := range siteBuildLogSteps {
		if !started[step.container] {
			continue
		}
		tail, err := r.stepTail(ctx, pod.Name, step.container)
		if err != nil {
			return "", fmt.Errorf("read the %s step's log: %w", step.title, err)
		}
		fmt.Fprintf(&out, "== %s ==\n%s\n", step.title, tail)
	}
	return out.String(), nil
}

// stepTail streams a step's log through a bounded tail, so a build printing huge lines cannot fill the controller's memory.
func (r *SiteBuildRunner) stepTail(ctx context.Context, pod, container string) (string, error) {
	stream, err := r.client.clientset.CoreV1().Pods(r.options.Namespace).GetLogs(pod, &corev1.PodLogOptions{
		Container: container, TailLines: pointerTo(int64(siteBuildLogTailLines)),
	}).Stream(ctx)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	tail := &tailWriter{limit: siteBuildLogStepBytes}
	if _, err := io.Copy(tail, stream); err != nil {
		return "", err
	}
	return tail.String(), nil
}

// tailWriter keeps at most twice limit bytes while writing, and the last limit when read.
type tailWriter struct {
	limit int
	data  []byte
	cut   bool
}

func (w *tailWriter) Write(chunk []byte) (int, error) {
	w.data = append(w.data, chunk...)
	if len(w.data) > 2*w.limit {
		w.data = append([]byte{}, w.data[len(w.data)-w.limit:]...)
		w.cut = true
	}
	return len(chunk), nil
}

func (w *tailWriter) String() string {
	text := keepTail(string(w.data), w.limit)
	if w.cut && !strings.HasPrefix(text, "…") {
		return "…" + text
	}
	return text
}

// keepTail keeps the last limit bytes, marking the cut.
func keepTail(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return "…" + text[len(text)-limit:]
}

// Remove deletes the Job with its pod; the Secrets go with it, and are deleted here too in case the owner link never took.
func (r *SiteBuildRunner) Remove(ctx context.Context, buildID string) error {
	name := siteBuildName(buildID)
	background := metav1.DeletePropagationBackground
	err := r.client.clientset.BatchV1().Jobs(r.options.Namespace).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &background})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete the build job: %w", err)
	}
	for _, secret := range []string{name + "-env", name + "-git"} {
		err := r.client.clientset.CoreV1().Secrets(r.options.Namespace).Delete(ctx, secret, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete the build's secret %s: %w", secret, err)
		}
	}
	return nil
}

// execStream runs a command in a container and streams its stdout into out.
func (c *Client) execStream(ctx context.Context, namespace, pod, container string, command []string, out io.Writer) error {
	request := c.clientset.CoreV1().RESTClient().Post().Resource("pods").Name(pod).Namespace(namespace).SubResource("exec").
		Param("container", container).Param("stdout", "true").Param("stderr", "true")
	for _, part := range command {
		request = request.Param("command", part)
	}
	executor, err := remotecommand.NewSPDYExecutor(c.restConfig, "POST", request.URL())
	if err != nil {
		return fmt.Errorf("create executor: %w", err)
	}
	var stderr strings.Builder
	if err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: out, Stderr: &stderr}); err != nil {
		if errors.Is(err, sitebuilds.ErrOutputTooLarge) {
			return err
		}
		return fmt.Errorf("exec: %w (stderr: %s)", err, keepTail(stderr.String(), 1024))
	}
	return nil
}
