package k8s

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

var testDiskJobs = DiskJobOptions{
	Image:        "busybox:1.37.0@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e",
	RuntimeClass: "gvisor",
	Timeout:      5 * time.Second,
}

// runJobsAs plays the Job controller: every created Job at once has one pod
// that ended with the exit code and termination message given.
func runJobsAs(clientset *fake.Clientset, exitCode int32, message string) *[]*batchv1.Job {
	created := &[]*batchv1.Job{}
	clientset.PrependReactor("create", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		job := action.(k8stesting.CreateAction).GetObject().(*batchv1.Job)
		*created = append(*created, job.DeepCopy())
		condition := batchv1.JobComplete
		if exitCode == 0 {
			job.Status.Succeeded = 1
		} else {
			job.Status.Failed = 1
			condition = batchv1.JobFailed
		}
		job.Status.Conditions = []batchv1.JobCondition{{Type: condition, Status: corev1.ConditionTrue}}
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: job.Name + "-x", Namespace: job.Namespace, Labels: map[string]string{"job-name": job.Name}},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "disk",
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: exitCode, Message: message}},
			}}},
		}
		if err := clientset.Tracker().Add(pod); err != nil {
			return true, nil, err
		}
		return false, nil, nil
	})
	// The fake has no garbage collector: deleting a Job takes its pods too.
	clientset.PrependReactor("delete", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		name := action.(k8stesting.DeleteAction).GetName()
		_ = clientset.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("pods"), action.GetNamespace(), name+"-x")
		return false, nil, nil
	})
	return created
}

func diskClaimOf(t *testing.T, clientset *fake.Clientset, name string) *corev1.PersistentVolumeClaim {
	t.Helper()
	claim, err := clientset.CoreV1().PersistentVolumeClaims(testNamespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("claim %s: %v", name, err)
	}
	return claim
}

func TestAppDiskClaimNameByGeneration(t *testing.T) {
	if got := AppDiskClaimName("app-01h", 0); got != "app-disk-app-01h" {
		t.Errorf("generation 0 = %q", got)
	}
	if got := AppDiskClaimName("app-01h", 2); got != "app-disk-app-01h-g2" {
		t.Errorf("generation 2 = %q", got)
	}
}

// A disk moved onto a smaller volume is mounted from that volume.
func TestRenderAppWorkloadMountsTheDisksGeneration(t *testing.T) {
	app := diskApp()
	app.Disk.Generation = 2
	workload := renderWithOptions(t, app, testRenderOptions)
	if workload.Disk.Name != "app-disk-app-01h-g2" {
		t.Fatalf("claim = %q, want the generation-2 volume", workload.Disk.Name)
	}
	claim := workload.Deployment.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName
	if claim != workload.Disk.Name {
		t.Fatalf("the Deployment mounts %q, want %q", claim, workload.Disk.Name)
	}
}

func TestAppDiskUsage_ReadsTheVolumesFilesystem(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	app := diskApp()
	deployedApp(t, c, app)
	jobs := runJobsAs(clientset, 0, "/dev/mapper/excalibase--tenants-pvc 1015704 102400 913304 10% /disk\n")

	usage, err := c.AppDiskUsage(context.Background(), testNamespace, app.ID, *app.Disk, testDiskJobs)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.UsedBytes != 102400*1024 || usage.SizeBytes != 1015704*1024 {
		t.Fatalf("usage = %+v, want 100Mi used of 1015704KiB", usage)
	}
	if len(*jobs) != 1 {
		t.Fatalf("%d jobs, want one probe", len(*jobs))
	}
	pod := (*jobs)[0].Spec.Template.Spec
	volume := pod.Volumes[0].PersistentVolumeClaim
	if volume == nil || volume.ClaimName != AppDiskClaimName(app.ID, 0) || !volume.ReadOnly {
		t.Fatalf("probe volume = %+v, want the app's claim read-only", volume)
	}
	container := pod.Containers[0]
	if container.SecurityContext == nil || container.SecurityContext.RunAsNonRoot == nil || !*container.SecurityContext.RunAsNonRoot {
		t.Error("the usage probe must run as non-root")
	}
	assertDiskJobPod(t, pod)
	if _, err := clientset.BatchV1().Jobs(testNamespace).Get(context.Background(), (*jobs)[0].Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("the probe Job must be removed once read: %v", err)
	}
}

// assertDiskJobPod: every disk job runs the pinned tools image in the app's
// sandbox, with no service account token, no network identity and no retry.
func assertDiskJobPod(t *testing.T, pod corev1.PodSpec) {
	t.Helper()
	if pod.RuntimeClassName == nil || *pod.RuntimeClassName != "gvisor" {
		t.Errorf("runtime class = %v, want the app sandbox", pod.RuntimeClassName)
	}
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Error("a disk job must not mount a service account token")
	}
	if pod.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("restart policy = %s", pod.RestartPolicy)
	}
	container := pod.Containers[0]
	if container.Image != testDiskJobs.Image {
		t.Errorf("image = %q", container.Image)
	}
	sc := container.SecurityContext
	if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation || sc.Capabilities == nil ||
		len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("security context = %+v, want no escalation and ALL dropped", sc)
	}
	if container.Resources.Limits.Memory().IsZero() || container.Resources.Limits.Cpu().IsZero() {
		t.Error("a disk job must have CPU and memory limits")
	}
}

func TestAppDiskUsage_NoClaimYet(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	jobs := runJobsAs(clientset, 0, "")
	_, err := c.AppDiskUsage(context.Background(), testNamespace, "app-01h", apphost.AppDisk{MountPath: "/data", Size: "5Gi"}, testDiskJobs)
	if !errors.Is(err, ErrAppDiskNotCreated) || len(*jobs) != 0 {
		t.Fatalf("err = %v with %d jobs, want ErrAppDiskNotCreated and no probe", err, len(*jobs))
	}
}

func TestAppDiskUsage_AnUnreadableAnswerIsAnError(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	app := diskApp()
	deployedApp(t, c, app)
	runJobsAs(clientset, 0, "garbage")
	if _, err := c.AppDiskUsage(context.Background(), testNamespace, app.ID, *app.Disk, testDiskJobs); !errors.Is(err, ErrAppDiskJob) {
		t.Fatalf("err = %v, want ErrAppDiskJob", err)
	}
}

func TestCopyAppDisk_CopiesOntoANewSmallerClaim(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	app := diskApp()
	deployedApp(t, c, app)
	jobs := runJobsAs(clientset, 0, "")
	to := apphost.AppDisk{MountPath: "/data", Size: "500Mi", Generation: 1}

	if err := c.CopyAppDisk(context.Background(), testNamespace, app, to, "excalibase-tenant", testDiskJobs); err != nil {
		t.Fatalf("copy: %v", err)
	}
	claim := diskClaimOf(t, clientset, AppDiskClaimName(app.ID, 1))
	if got := claim.Spec.Resources.Requests[corev1.ResourceStorage]; got.Cmp(resource.MustParse("500Mi")) != 0 {
		t.Errorf("new claim asks %s, want 500Mi", got.String())
	}
	if claim.Spec.StorageClassName == nil || *claim.Spec.StorageClassName != "excalibase-tenant" {
		t.Errorf("new claim class = %v", claim.Spec.StorageClassName)
	}
	if claim.Labels["excalibase.io/app"] != app.ID {
		t.Errorf("new claim labels = %v, want the app's so its deletion takes it", claim.Labels)
	}
	pod := (*jobs)[0].Spec.Template.Spec
	mounts := map[string]corev1.PersistentVolumeClaimVolumeSource{}
	for _, volume := range pod.Volumes {
		mounts[volume.Name] = *volume.PersistentVolumeClaim
	}
	if from := mounts["from"]; from.ClaimName != AppDiskClaimName(app.ID, 0) || !from.ReadOnly {
		t.Errorf("source = %+v, want the current claim read-only", from)
	}
	if into := mounts["to"]; into.ClaimName != AppDiskClaimName(app.ID, 1) || into.ReadOnly {
		t.Errorf("target = %+v, want the new claim writable", into)
	}
	assertDiskJobPod(t, pod)
	caps := pod.Containers[0].SecurityContext.Capabilities.Add
	for _, capability := range caps {
		if capability == "SYS_ADMIN" || capability == "MKNOD" {
			t.Errorf("the copy must not get %s", capability)
		}
	}
}

// A copy that does not fit fails with the tool's reason and leaves no new claim behind.
func TestCopyAppDisk_FailureRemovesTheNewClaim(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	app := diskApp()
	deployedApp(t, c, app)
	runJobsAs(clientset, 1, "cp: write error: No space left on device")
	to := apphost.AppDisk{MountPath: "/data", Size: "500Mi", Generation: 1}

	err := c.CopyAppDisk(context.Background(), testNamespace, app, to, "excalibase-tenant", testDiskJobs)
	if !errors.Is(err, ErrAppDiskJob) || !strings.Contains(err.Error(), "No space left on device") {
		t.Fatalf("err = %v, want ErrAppDiskJob with the tool's reason", err)
	}
	if _, err := clientset.CoreV1().PersistentVolumeClaims(testNamespace).Get(context.Background(), AppDiskClaimName(app.ID, 1), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the new claim must be removed after a failed copy: %v", err)
	}
	diskClaimOf(t, clientset, AppDiskClaimName(app.ID, 0))
}

func TestDeleteOtherAppDisks_KeepsOnlyTheCurrentGeneration(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	app := diskApp()
	deployedApp(t, c, app)
	runJobsAs(clientset, 0, "")
	if err := c.CopyAppDisk(context.Background(), testNamespace, app, apphost.AppDisk{MountPath: "/data", Size: "1Gi", Generation: 1}, "", testDiskJobs); err != nil {
		t.Fatal(err)
	}
	other := diskApp()
	other.ID = "app-02h"
	other.Name = "other"
	deployedApp(t, c, other)

	if err := c.RepointAppDisk(context.Background(), testNamespace, app.ID, AppDiskClaimName(app.ID, 1)); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteOtherAppDisks(context.Background(), testNamespace, app.ID, 1, shortWait); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := clientset.CoreV1().PersistentVolumeClaims(testNamespace).Get(context.Background(), AppDiskClaimName(app.ID, 0), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("the old generation must go: %v", err)
	}
	diskClaimOf(t, clientset, AppDiskClaimName(app.ID, 1))
	diskClaimOf(t, clientset, AppDiskClaimName(other.ID, 0))
}

// A stopped app resumes by scaling its Deployment back, so the Deployment is
// pointed at the new volume before the old one goes.
func TestRepointAppDisk_MountsTheNewClaim(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	app := diskApp()
	deployedApp(t, c, app)
	if err := c.RepointAppDisk(context.Background(), testNamespace, app.ID, AppDiskClaimName(app.ID, 3)); err != nil {
		t.Fatalf("repoint: %v", err)
	}
	dep, err := clientset.AppsV1().Deployments(testNamespace).Get(context.Background(), AppObjectName(app.Name), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := diskVolumeClaim(dep); got != AppDiskClaimName(app.ID, 3) {
		t.Fatalf("Deployment mounts %q, want generation 3", got)
	}
}

func diskVolumeClaim(dep *appsv1.Deployment) string {
	for _, volume := range dep.Spec.Template.Spec.Volumes {
		if volume.Name == appDiskVolumeName && volume.PersistentVolumeClaim != nil {
			return volume.PersistentVolumeClaim.ClaimName
		}
	}
	return ""
}

func TestParseDiskUsage(t *testing.T) {
	usage, err := parseDiskUsage("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/x 2048 1024 1024 50% /disk\n")
	if err != nil || usage.UsedBytes != 1024*1024 || usage.SizeBytes != 2048*1024 {
		t.Fatalf("usage = %+v, %v", usage, err)
	}
	for _, bad := range []string{"", "/dev/x 2048", "/dev/x a b c 1% /disk", "/dev/x -1 1 1 1% /disk"} {
		if _, err := parseDiskUsage(bad); err == nil {
			t.Errorf("parsed %q", bad)
		}
	}
}

// A new disk's root is opened to any user, as local-path made it, so a
// non-root image can write to it; an existing disk is left as it is.
func TestCreateAppDisk_OpensANewDisksRootOnce(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	jobs := runJobsAs(clientset, 0, "")
	app := diskApp()
	if err := c.CreateAppDisk(context.Background(), testNamespace, app, "excalibase-tenant", testDiskJobs); err != nil {
		t.Fatalf("create: %v", err)
	}
	diskClaimOf(t, clientset, AppDiskClaimName(app.ID, 0))
	if len(*jobs) != 1 || !strings.Contains(strings.Join((*jobs)[0].Spec.Template.Spec.Containers[0].Command, " "), "chmod 1777") {
		t.Fatalf("jobs = %d, want one opening the disk's root", len(*jobs))
	}
	assertDiskJobPod(t, (*jobs)[0].Spec.Template.Spec)
	if err := c.CreateAppDisk(context.Background(), testNamespace, app, "excalibase-tenant", testDiskJobs); err != nil || len(*jobs) != 1 {
		t.Fatalf("an existing disk: err %v, %d jobs; want it left alone", err, len(*jobs))
	}
}

func TestCreateAppDisk_AFailedInitRemovesTheClaim(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	runJobsAs(clientset, 1, "chmod: /disk: Operation not permitted")
	app := diskApp()
	if err := c.CreateAppDisk(context.Background(), testNamespace, app, "", testDiskJobs); !errors.Is(err, ErrAppDiskJob) {
		t.Fatalf("err = %v, want ErrAppDiskJob", err)
	}
	if _, err := clientset.CoreV1().PersistentVolumeClaims(testNamespace).Get(context.Background(), AppDiskClaimName(app.ID, 0), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the claim must go with a failed init: %v", err)
	}
}

// A volume the running app has mounted cannot be mounted by a second pod
// (LVM LocalPV refuses it), so the running app's own container is asked.
func TestAppDiskUsage_AsksTheRunningAppItself(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	app := diskApp()
	deployedApp(t, c, app)
	jobs := runJobsAs(clientset, 0, "")
	pod := appPodObject(app, "web-1")
	pod.Status.Phase = corev1.PodRunning
	pod.Spec.Containers = []corev1.Container{{Name: app.Name}}
	if _, err := clientset.CoreV1().Pods(testNamespace).Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	var ran []string
	c.diskExec = func(_ context.Context, namespace, podName, container string, cmd []string) (string, error) {
		ran = append(ran, namespace+"/"+podName+"/"+container+":"+strings.Join(cmd, " "))
		return "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/x 2048 1024 1024 50% /data\n", nil
	}
	usage, err := c.AppDiskUsage(context.Background(), testNamespace, app.ID, *app.Disk, testDiskJobs)
	if err != nil || usage.UsedBytes != 1024*1024 {
		t.Fatalf("usage %+v, %v", usage, err)
	}
	if len(ran) != 1 || ran[0] != testNamespace+"/web-1/"+app.Name+":df -P -k /data" || len(*jobs) != 0 {
		t.Fatalf("ran %v with %d jobs, want one df in the app and no probe", ran, len(*jobs))
	}
	c.diskExec = func(context.Context, string, string, string, []string) (string, error) {
		return "", errors.New("exec: executable file not found: df")
	}
	if _, err := c.AppDiskUsage(context.Background(), testNamespace, app.ID, *app.Disk, testDiskJobs); !errors.Is(err, ErrAppDiskUsageUnavailable) {
		t.Fatalf("an image without df: err = %v, want ErrAppDiskUsageUnavailable", err)
	}
}

// A claim a Deployment of the app still mounts is never removed as "old":
// after an interrupted move the Deployment, not the record, may be right.
func TestDeleteOtherAppDisks_KeepsAClaimTheAppStillMounts(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	app := diskApp()
	deployedApp(t, c, app)
	runJobsAs(clientset, 0, "")
	if err := c.CopyAppDisk(context.Background(), testNamespace, app, apphost.AppDisk{MountPath: "/data", Size: "1Gi", Generation: 1}, "", testDiskJobs); err != nil {
		t.Fatal(err)
	}
	if err := c.RepointAppDisk(context.Background(), testNamespace, app.ID, AppDiskClaimName(app.ID, 1)); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteOtherAppDisks(context.Background(), testNamespace, app.ID, 0, shortWait); !errors.Is(err, ErrAppDiskInUse) {
		t.Fatalf("err = %v, want ErrAppDiskInUse", err)
	}
	diskClaimOf(t, clientset, AppDiskClaimName(app.ID, 1))
}

// A copy or probe left running by a crash would hold the claim it mounts, so
// the app's disk Jobs go before any claim does.
func TestDeleteOtherAppDisks_RemovesTheAppsDiskJobsFirst(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	app := diskApp()
	deployedApp(t, c, app)
	stale := diskJob(testNamespace, diskJobCopyPrefix+app.ID, app.ID, testDiskJobs, copyScript, nil, nil, &corev1.SecurityContext{})
	if _, err := clientset.BatchV1().Jobs(testNamespace).Create(context.Background(), stale, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteOtherAppDisks(context.Background(), testNamespace, app.ID, 0, shortWait); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := clientset.BatchV1().Jobs(testNamespace).Get(context.Background(), stale.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the stale copy Job must go: %v", err)
	}
}
