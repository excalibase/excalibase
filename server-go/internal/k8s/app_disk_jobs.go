package k8s

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

// Two short Jobs read and move an app's disk (the owner's downgrade rule,
// 2026-09-29): a probe that reads how much of the volume is used, and a copy
// that moves the disk onto a smaller volume. Both run the pinned tools image in
// the app's own sandbox, because they read what the tenant wrote.

// DiskJobOptions are how the app-disk Jobs run.
type DiskJobOptions struct {
	// Image is the tools image (busybox), pinned by digest.
	Image string
	// RuntimeClass is the app sandbox.
	RuntimeClass string
	// Timeout bounds one Job, start to finish.
	Timeout time.Duration
}

// AppDiskUsage is what the volume's filesystem reports.
type AppDiskUsage struct {
	UsedBytes int64 `json:"usedBytes"`
	SizeBytes int64 `json:"sizeBytes"`
}

// ErrAppDiskJob is a probe or copy that did not finish with a readable answer.
var ErrAppDiskJob = errors.New("app disk job failed")

const (
	diskJobUsagePrefix = "app-disk-usage-"
	diskJobCopyPrefix  = "app-disk-copy-"
	diskJobInitPrefix  = "app-disk-init-"
	diskJobLabel       = "excalibase.io/app-disk-job"
	diskJobContainer   = "disk"
	diskMountPath      = "/disk"
	// The message a container leaves is capped by the kubelet at 4096 bytes.
	diskJobMessageLimit = 1024
	diskJobTTLSeconds   = int32(300)
	// A probe only runs df; the copy's timeout is for moving a whole disk.
	diskProbeTimeout = 2 * time.Minute
	// Enough to probe or copy a small disk; a copy is bounded by its Timeout.
	diskJobCPU    = "250m"
	diskJobMemory = "64Mi"
	// usageScript writes the volume's POSIX df line (KiB blocks) as the
	// container's termination message, so reading it needs no log access.
	usageScript = "df -P -k " + diskMountPath + " | tail -n 1 > /dev/termination-log"
	// copyScript keeps owners, modes, times and links; a failure leaves the
	// tool's own reason (such as ENOSPC) as the termination message.
	copyScript = "cp -a /from/. /to/ 2> /dev/termination-log && sync"
	// initScript opens a new disk's root to any user, as local-path made it:
	// images often run as a non-root user the platform cannot know.
	initScript = "chmod 1777 " + diskMountPath + " 2> /dev/termination-log"
)

func (o DiskJobOptions) validate() error {
	if o.Image == "" || o.RuntimeClass == "" || o.Timeout <= 0 {
		return fmt.Errorf("%w: the tools image, sandbox runtime class and timeout are all required", ErrAppDiskJob)
	}
	return nil
}

// ErrAppDiskUsageUnavailable: the running app could not report its disk (its
// image has no df), and its volume cannot be read beside it.
var ErrAppDiskUsageUnavailable = errors.New("the running app's disk usage cannot be read")

// AppDiskUsage reads how much of the app's disk is used. The volume admits one
// pod at a time, so a running app is asked through its own container, and a
// stopped one's volume is read by a short probe. ErrAppDiskNotCreated when no
// deploy has made the disk yet.
func (c *Client) AppDiskUsage(ctx context.Context, namespace, appID string, disk apphost.AppDisk, opts DiskJobOptions) (AppDiskUsage, error) {
	claim := AppDiskClaimName(appID, disk.Generation)
	if err := c.requireClaim(ctx, namespace, claim); err != nil {
		return AppDiskUsage{}, err
	}
	pod, err := c.runningAppPod(ctx, namespace, appID)
	if err != nil {
		return AppDiskUsage{}, err
	}
	if pod != nil {
		return c.usageFromApp(ctx, namespace, pod, disk.MountPath)
	}
	if err := opts.validate(); err != nil {
		return AppDiskUsage{}, err
	}
	if opts.Timeout > diskProbeTimeout {
		opts.Timeout = diskProbeTimeout
	}
	nonRoot := int64(65534)
	job := diskJob(namespace, diskJobUsagePrefix+appID, appID, opts, usageScript,
		[]corev1.Volume{claimVolume("disk", claim, true)},
		[]corev1.VolumeMount{{Name: "disk", MountPath: diskMountPath, ReadOnly: true}},
		&corev1.SecurityContext{RunAsNonRoot: pointerTo(true), RunAsUser: &nonRoot, RunAsGroup: &nonRoot})
	message, err := c.runDiskJob(ctx, namespace, job, opts.Timeout)
	if err != nil {
		return AppDiskUsage{}, err
	}
	return parseDiskUsage(message)
}

func (c *Client) runningAppPod(ctx context.Context, namespace, appID string) (*corev1.Pod, error) {
	pods, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: appOwnedSelector(appID)})
	if err != nil {
		return nil, fmt.Errorf("list app pods: %w", err)
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp == nil && len(pod.Spec.Containers) > 0 {
			return pod, nil
		}
	}
	return nil, nil
}

func (c *Client) usageFromApp(ctx context.Context, namespace string, pod *corev1.Pod, mountPath string) (AppDiskUsage, error) {
	execute := c.diskExec
	if execute == nil {
		execute = c.ExecInPod
	}
	output, err := execute(ctx, namespace, pod.Name, pod.Spec.Containers[0].Name, []string{"df", "-P", "-k", mountPath})
	if err == nil {
		var usage AppDiskUsage
		if usage, err = parseDiskUsage(output); err == nil {
			return usage, nil
		}
	}
	log.Printf("app disk usage %s/%s: %v", namespace, pod.Name, err)
	return AppDiskUsage{}, fmt.Errorf("%w: the app's image has no usable df; stop the app to measure it", ErrAppDiskUsageUnavailable)
}

// CopyAppDisk copies the app's current disk onto a new claim for to, which
// names a later generation. The app must be stopped: nothing may write while
// it copies. A failed copy removes the new claim; the current one is untouched.
func (c *Client) CopyAppDisk(ctx context.Context, namespace string, app *apphost.App, to apphost.AppDisk, storageClass string, opts DiskJobOptions) error {
	if err := opts.validate(); err != nil {
		return err
	}
	if app.Disk == nil || to.Generation <= app.Disk.Generation {
		return fmt.Errorf("%w: a copy goes to a later generation of an existing disk", ErrAppDiskJob)
	}
	from := AppDiskClaimName(app.ID, app.Disk.Generation)
	source, err := c.clientset.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, from, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return ErrAppDiskNotCreated
	}
	if err != nil {
		return fmt.Errorf("read app disk: %w", err)
	}
	target, err := buildAppDiskClaim(namespace, app, to, storageClass)
	if err != nil {
		return err
	}
	if err := c.ensureAppDiskReplacing(ctx, namespace, target, claimBytes(source)); err != nil {
		return err
	}
	// Root with only the file-ownership capabilities: it must read and recreate
	// files of any owner. No MKNOD, so a device node on the disk is not copied.
	root := int64(0)
	job := diskJob(namespace, diskJobCopyPrefix+app.ID, app.ID, opts, copyScript,
		[]corev1.Volume{claimVolume("from", from, true), claimVolume("to", target.Name, false)},
		[]corev1.VolumeMount{{Name: "from", MountPath: "/from", ReadOnly: true}, {Name: "to", MountPath: "/to"}},
		&corev1.SecurityContext{RunAsUser: &root, Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
			Add:  []corev1.Capability{"CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID"},
		}})
	if _, err := c.runDiskJob(ctx, namespace, job, opts.Timeout); err != nil {
		if delErr := c.deleteClaimAndWait(ctx, namespace, target.Name, opts.Timeout); delErr != nil {
			return errors.Join(err, fmt.Errorf("remove the unfinished copy: %w", delErr))
		}
		return err
	}
	return nil
}

// CreateAppDisk makes the app's disk once, before its first deploy mounts it,
// and opens its root to any user. An existing disk is left as it is.
func (c *Client) CreateAppDisk(ctx context.Context, namespace string, app *apphost.App, storageClass string, opts DiskJobOptions) error {
	if app.Disk == nil {
		return nil
	}
	name := AppDiskClaimName(app.ID, app.Disk.Generation)
	err := c.requireClaim(ctx, namespace, name)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrAppDiskNotCreated) {
		return err
	}
	if err := opts.validate(); err != nil {
		return err
	}
	claim, err := buildAppDiskClaim(namespace, app, *app.Disk, storageClass)
	if err != nil {
		return err
	}
	if err := c.ensureAppDisk(ctx, namespace, claim); err != nil {
		return err
	}
	root := int64(0)
	job := diskJob(namespace, diskJobInitPrefix+app.ID, app.ID, opts, initScript,
		[]corev1.Volume{claimVolume("disk", name, false)},
		[]corev1.VolumeMount{{Name: "disk", MountPath: diskMountPath}},
		&corev1.SecurityContext{RunAsUser: &root})
	if _, err := c.runDiskJob(ctx, namespace, job, opts.Timeout); err != nil {
		if delErr := c.deleteClaimAndWait(ctx, namespace, name, opts.Timeout); delErr != nil {
			return errors.Join(err, fmt.Errorf("remove the new disk: %w", delErr))
		}
		return err
	}
	return nil
}

// ErrAppDiskInUse refuses to remove a claim a Deployment of the app still mounts.
var ErrAppDiskInUse = errors.New("the app still mounts that disk")

// DeleteOtherAppDisks removes every claim of the app except generation keep:
// the volume a lowered disk left behind, or an unfinished copy's. The app's
// disk Jobs go first, since one left running holds the claim it mounts, and a
// claim a Deployment of the app still mounts is never removed.
func (c *Client) DeleteOtherAppDisks(ctx context.Context, namespace, appID string, keep int, timeout time.Duration) error {
	if err := c.deleteAppDiskJobs(ctx, namespace, appID, timeout); err != nil {
		return err
	}
	claims, err := c.clientset.CoreV1().PersistentVolumeClaims(namespace).List(ctx,
		metav1.ListOptions{LabelSelector: appOwnedSelector(appID)})
	if err != nil {
		return fmt.Errorf("list app disks: %w", err)
	}
	mounted, err := c.claimsMountedBy(ctx, namespace, appID)
	if err != nil {
		return err
	}
	current := AppDiskClaimName(appID, keep)
	for _, claim := range claims.Items {
		if claim.Name == current || !strings.HasPrefix(claim.Name, appDiskPrefix+appID) {
			continue
		}
		if mounted[claim.Name] {
			return fmt.Errorf("%w: %s", ErrAppDiskInUse, claim.Name)
		}
		if err := c.deleteClaimAndWait(ctx, namespace, claim.Name, timeout); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) deleteAppDiskJobs(ctx context.Context, namespace, appID string, timeout time.Duration) error {
	jobs, err := c.clientset.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{LabelSelector: diskJobLabel + "=" + appID})
	if err != nil {
		return fmt.Errorf("list app disk jobs: %w", err)
	}
	for _, job := range jobs.Items {
		if err := c.deleteDiskJob(ctx, namespace, job.Name, timeout); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) claimsMountedBy(ctx context.Context, namespace, appID string) (map[string]bool, error) {
	deployments, err := c.clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{LabelSelector: appOwnedSelector(appID)})
	if err != nil {
		return nil, fmt.Errorf("list app deployments: %w", err)
	}
	mounted := map[string]bool{}
	for _, dep := range deployments.Items {
		for _, volume := range dep.Spec.Template.Spec.Volumes {
			if volume.PersistentVolumeClaim != nil {
				mounted[volume.PersistentVolumeClaim.ClaimName] = true
			}
		}
	}
	return mounted, nil
}

// RepointAppDisk mounts claim in every Deployment of the app, so a resume of a
// stopped app starts on the disk's current volume.
func (c *Client) RepointAppDisk(ctx context.Context, namespace, appID, claim string) error {
	names, err := c.appDeploymentNames(ctx, namespace, appID)
	if errors.Is(err, ErrAppNotDeployed) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := c.updateAppDeployment(ctx, namespace, name, func(dep *appsv1.Deployment) error {
			for i := range dep.Spec.Template.Spec.Volumes {
				volume := &dep.Spec.Template.Spec.Volumes[i]
				if volume.Name == appDiskVolumeName && volume.PersistentVolumeClaim != nil {
					volume.PersistentVolumeClaim.ClaimName = claim
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) requireClaim(ctx context.Context, namespace, name string) error {
	_, err := c.clientset.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return ErrAppDiskNotCreated
	}
	if err != nil {
		return fmt.Errorf("read app disk: %w", err)
	}
	return nil
}

func (c *Client) deleteClaimAndWait(ctx context.Context, namespace, name string, timeout time.Duration) error {
	claims := c.clientset.CoreV1().PersistentVolumeClaims(namespace)
	if err := claims.Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete app disk %s: %w", name, err)
	}
	return waitUntilNoneLeft(ctx, timeout, func(ctx context.Context) ([]string, error) {
		_, err := claims.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read app disk %s: %w", name, err)
		}
		return []string{name}, nil
	})
}

func claimVolume(name, claim string, readOnly bool) corev1.Volume {
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{
		PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim, ReadOnly: readOnly},
	}}
}

func diskJob(namespace, name, appID string, opts DiskJobOptions, script string,
	volumes []corev1.Volume, mounts []corev1.VolumeMount, security *corev1.SecurityContext) *batchv1.Job {
	security.AllowPrivilegeEscalation = pointerTo(false)
	security.ReadOnlyRootFilesystem = pointerTo(true)
	security.SeccompProfile = &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}
	if security.Capabilities == nil {
		security.Capabilities = &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}
	}
	limits := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(diskJobCPU),
		corev1.ResourceMemory: resource.MustParse(diskJobMemory),
	}
	labels := map[string]string{
		"app.kubernetes.io/managed-by": appManagedByValue,
		diskJobLabel:                   appID,
	}
	deadline := int64(opts.Timeout / time.Second)
	runtimeClass := opts.RuntimeClass
	ttl := diskJobTTLSeconds
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit:            pointerTo[int32](0),
			ActiveDeadlineSeconds:   &deadline,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					RuntimeClassName:             &runtimeClass,
					AutomountServiceAccountToken: pointerTo(false),
					EnableServiceLinks:           pointerTo(false),
					Volumes:                      volumes,
					Containers: []corev1.Container{{
						Name:                     diskJobContainer,
						Image:                    opts.Image,
						Command:                  []string{"sh", "-c", script},
						VolumeMounts:             mounts,
						SecurityContext:          security,
						TerminationMessagePolicy: corev1.TerminationMessageReadFile,
						Resources:                corev1.ResourceRequirements{Requests: limits, Limits: limits},
					}},
				},
			},
		},
	}
}

// runDiskJob starts job (replacing a finished one of the same name), waits for
// it, removes it and returns its container's termination message.
func (c *Client) runDiskJob(ctx context.Context, namespace string, job *batchv1.Job, timeout time.Duration) (string, error) {
	if problems := validation.IsDNS1123Label(job.Name); len(problems) > 0 {
		return "", fmt.Errorf("%w: cannot name the job %q: %s", ErrAppDiskJob, job.Name, strings.Join(problems, "; "))
	}
	if err := c.deleteDiskJob(ctx, namespace, job.Name, timeout); err != nil {
		return "", err
	}
	jobs := c.clientset.BatchV1().Jobs(namespace)
	if _, err := jobs.Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("start %s: %w", job.Name, err)
	}
	succeeded, waitErr := c.waitForDiskJob(ctx, namespace, job.Name, timeout)
	message := c.diskJobMessage(ctx, namespace, job.Name)
	if err := c.deleteDiskJob(ctx, namespace, job.Name, timeout); err != nil {
		return "", err
	}
	if waitErr != nil {
		log.Printf("app disk job %s/%s: %v", namespace, job.Name, waitErr)
		return "", fmt.Errorf("%w: it did not finish in time", ErrAppDiskJob)
	}
	if !succeeded {
		return "", fmt.Errorf("%w: %s", ErrAppDiskJob, message)
	}
	return message, nil
}

func (c *Client) waitForDiskJob(ctx context.Context, namespace, name string, timeout time.Duration) (bool, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var succeeded bool
	err := wait.PollUntilContextCancel(waitCtx, appRolloutPollInterval, true, func(pollCtx context.Context) (bool, error) {
		job, err := c.clientset.BatchV1().Jobs(namespace).Get(pollCtx, name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Errorf("read %s: %w", name, err)
		}
		for _, condition := range job.Status.Conditions {
			if condition.Status != corev1.ConditionTrue {
				continue
			}
			switch condition.Type {
			case batchv1.JobComplete:
				succeeded = true
				return true, nil
			case batchv1.JobFailed:
				return true, nil
			}
		}
		return false, nil
	})
	return succeeded, err
}

func (c *Client) diskJobMessage(ctx context.Context, namespace, name string) string {
	pods, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + name})
	if err != nil {
		log.Printf("app disk job %s/%s: read its pod: %v", namespace, name, err)
		return "its result could not be read"
	}
	for _, pod := range pods.Items {
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == diskJobContainer && status.State.Terminated != nil {
				message := strings.TrimSpace(status.State.Terminated.Message)
				if len(message) > diskJobMessageLimit {
					message = message[:diskJobMessageLimit]
				}
				if message == "" && status.State.Terminated.ExitCode != 0 {
					return "exit code " + strconv.Itoa(int(status.State.Terminated.ExitCode)) + ", " + status.State.Terminated.Reason
				}
				return message
			}
		}
	}
	return "its pod left no result"
}

func (c *Client) deleteDiskJob(ctx context.Context, namespace, name string, timeout time.Duration) error {
	background := metav1.DeletePropagationBackground
	err := c.clientset.BatchV1().Jobs(namespace).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &background})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("remove %s: %w", name, err)
	}
	return c.waitForPodsGone(ctx, namespace, "job-name="+name, timeout)
}

// parseDiskUsage reads the last line of `df -P -k`: filesystem, 1024-blocks,
// used, available, capacity, mount point.
func parseDiskUsage(output string) (AppDiskUsage, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 6 {
		return AppDiskUsage{}, fmt.Errorf("%w: unreadable filesystem usage %q", ErrAppDiskJob, output)
	}
	size, sizeErr := strconv.ParseInt(fields[1], 10, 64)
	used, usedErr := strconv.ParseInt(fields[2], 10, 64)
	if sizeErr != nil || usedErr != nil || size <= 0 || used < 0 {
		return AppDiskUsage{}, fmt.Errorf("%w: unreadable filesystem usage %q", ErrAppDiskJob, output)
	}
	return AppDiskUsage{UsedBytes: used * 1024, SizeBytes: size * 1024}, nil
}

func pointerTo[T any](value T) *T { return &value }
