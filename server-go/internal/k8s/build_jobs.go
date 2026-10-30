package k8s

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

// cloneSecretSuffix names the Secret holding a build's clone credential, which the clone step alone reads.
const cloneSecretSuffix = "-git"

// gvisorHandler is the containerd handler every install registers for gVisor (rke2/node, single-host, tests).
const gvisorHandler = "runsc"

// buildLogStep is a step whose output a build's history keeps, under its title.
type buildLogStep struct{ container, title string }

// buildJobs is what site (EXC-354) and app (EXC-547) builds share: a Job created
// suspended until the Secrets it reads exist, its newest pod, the collect
// step's output, each step's log tail, and removal.
type buildJobs struct {
	client    *Client
	namespace string
	// prefix names the Job and its Secrets: prefix + build id.
	prefix string
	// secretSuffixes are the build's Secrets, named prefix + id + suffix.
	secretSuffixes []string
	steps          []buildLogStep
	collectStep    string
	archivePath    string
	stream         streamFunc
	// component is the pods' component label; the pod lookup matches it with the build id.
	component string
	// kindOf names a failing step's failure kind.
	kindOf func(step string, code int32) sitebuilds.FailureKind
}

func (b *buildJobs) name(buildID string) string { return b.prefix + buildID }

// create makes the Job suspended, then its Secrets owned by it, then resumes it.
// A start that fails part way removes what it made, so no suspended Job holds a slot.
func (b *buildJobs) create(ctx context.Context, buildID string, job *batchv1.Job, secrets []*corev1.Secret) error {
	jobs := b.client.clientset.BatchV1().Jobs(b.namespace)
	created, err := jobs.Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create the build job: %w", err)
	}
	if err := b.ownAndResume(ctx, created, secrets); err != nil {
		return errors.Join(err, b.Remove(context.WithoutCancel(ctx), buildID))
	}
	return nil
}

func (b *buildJobs) ownAndResume(ctx context.Context, created *batchv1.Job, secrets []*corev1.Secret) error {
	jobs := b.client.clientset.BatchV1().Jobs(b.namespace)
	owner := metav1.OwnerReference{APIVersion: "batch/v1", Kind: "Job", Name: created.Name, UID: created.UID, Controller: pointerTo(true)}
	for _, secret := range secrets {
		secret.OwnerReferences = []metav1.OwnerReference{owner}
		if _, err := b.client.clientset.CoreV1().Secrets(b.namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create the build's secret %s: %w", secret.Name, err)
		}
	}
	resume := []byte(`{"spec":{"suspend":false}}`)
	if _, err := jobs.Patch(ctx, created.Name, types.MergePatchType, resume, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("resume the build job: %w", err)
	}
	return nil
}

// Status reads the build's Job and newest pod; a Job gone from the cluster is a failed build.
func (b *buildJobs) Status(ctx context.Context, buildID string) (sitebuilds.RunStatus, error) {
	job, err := b.client.clientset.BatchV1().Jobs(b.namespace).Get(ctx, b.name(buildID), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return failed(sitebuilds.FailPlatform, 0, "the build job is gone from the cluster"), nil
	}
	if err != nil {
		return sitebuilds.RunStatus{}, fmt.Errorf("read the build job: %w", err)
	}
	pod, err := b.buildPod(ctx, buildID)
	if err != nil {
		return sitebuilds.RunStatus{}, err
	}
	return interpretBuild(job, pod, b.kindOf), nil
}

// buildPod is the build's newest pod, or nil before the Job has made one.
func (b *buildJobs) buildPod(ctx context.Context, buildID string) (*corev1.Pod, error) {
	pods, err := b.client.clientset.CoreV1().Pods(b.namespace).List(ctx, metav1.ListOptions{LabelSelector: siteBuildComponentKey + "=" + b.component + "," + siteBuildLabelBuild + "=" + buildID})
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

// Collect streams the packed output from the collect step, failing past limit bytes.
func (b *buildJobs) Collect(ctx context.Context, buildID string, w io.Writer, limit int64) error {
	pod, err := b.buildPod(ctx, buildID)
	if err != nil {
		return err
	}
	if pod == nil {
		return errors.New("the build has no pod to collect from")
	}
	capped := &cappedWriter{out: w, limit: limit}
	err = b.stream(ctx, b.namespace, pod.Name, b.collectStep, []string{"cat", b.archivePath}, capped)
	if capped.overflowed {
		return sitebuilds.ErrOutputTooLarge
	}
	if err != nil {
		return fmt.Errorf("read the packed output: %w", err)
	}
	return nil
}

// Logs answers each started step's tail, under a header per step.
func (b *buildJobs) Logs(ctx context.Context, buildID string) (string, error) {
	pod, err := b.buildPod(ctx, buildID)
	if err != nil || pod == nil {
		return "", err
	}
	started := map[string]bool{}
	for _, status := range allStatuses(pod) {
		started[status.Name] = status.State.Running != nil || status.State.Terminated != nil
	}
	var out strings.Builder
	for _, step := range b.steps {
		if !started[step.container] {
			continue
		}
		tail, err := b.stepTail(ctx, pod.Name, step.container)
		if err != nil {
			return "", fmt.Errorf("read the %s step's log: %w", step.title, err)
		}
		fmt.Fprintf(&out, "== %s ==\n%s\n", step.title, tail)
	}
	return out.String(), nil
}

// stepTail streams a step's log through a bounded tail, so a build printing huge lines cannot fill the controller's memory.
func (b *buildJobs) stepTail(ctx context.Context, pod, container string) (string, error) {
	stream, err := b.client.clientset.CoreV1().Pods(b.namespace).GetLogs(pod, &corev1.PodLogOptions{
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

// Remove deletes the Job with its pod; the Secrets go with it, and are deleted here too in case the owner link never took.
func (b *buildJobs) Remove(ctx context.Context, buildID string) error {
	name := b.name(buildID)
	background := metav1.DeletePropagationBackground
	err := b.client.clientset.BatchV1().Jobs(b.namespace).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &background})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete the build job: %w", err)
	}
	for _, suffix := range b.secretSuffixes {
		err := b.client.clientset.CoreV1().Secrets(b.namespace).Delete(ctx, name+suffix, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete the build's secret %s: %w", name+suffix, err)
		}
	}
	return nil
}

// ForgetCloneCredential deletes the clone credential's Secret once the clone is done; one already gone is not an error.
func (b *buildJobs) ForgetCloneCredential(ctx context.Context, buildID string) error {
	name := b.name(buildID) + cloneSecretSuffix
	err := b.client.clientset.CoreV1().Secrets(b.namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete the build's clone secret: %w", err)
	}
	return nil
}

// applyBuildQuota and applyBuildConfigMap converge a build namespace's shared objects on every start.
func applyBuildQuota(ctx context.Context, client *Client, desired *corev1.ResourceQuota) error {
	quotas := client.clientset.CoreV1().ResourceQuotas(desired.Namespace)
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

func applyBuildConfigMap(ctx context.Context, client *Client, desired *corev1.ConfigMap) error {
	maps := client.clientset.CoreV1().ConfigMaps(desired.Namespace)
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

// sandboxRuntimeReady refuses a RuntimeClass that is not gVisor: a build pod outside the sandbox must never start.
func sandboxRuntimeReady(ctx context.Context, client *Client, runtimeClass string) error {
	class, err := client.clientset.NodeV1().RuntimeClasses().Get(ctx, runtimeClass, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("the build sandbox RuntimeClass %q: %w", runtimeClass, err)
	}
	if class.Handler != gvisorHandler {
		return fmt.Errorf("the build RuntimeClass %q runs %q, not gVisor (%s)", runtimeClass, class.Handler, gvisorHandler)
	}
	return nil
}

// buildNamespaceReady refuses a namespace the chart did not make: builds never create one on the fly.
func buildNamespaceReady(ctx context.Context, client *Client, namespace string) error {
	if _, err := client.clientset.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{}); err != nil {
		return fmt.Errorf("the build namespace %s: %w", namespace, err)
	}
	return nil
}
