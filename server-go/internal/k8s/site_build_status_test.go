package k8s

import (
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

func terminated(name, reason string, code int32) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: reason, ExitCode: code}}}
}

func running(name string, ready bool) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, Ready: ready, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
}

func waiting(name, reason string) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}}}
}

func passedInits() []corev1.ContainerStatus {
	return []corev1.ContainerStatus{
		terminated("prepare-workdir", "Completed", 0), terminated("wait-for-fence", "Completed", 0),
		terminated("clone", "Completed", 0), terminated("build", "Completed", 0),
	}
}

func buildPod(phase corev1.PodPhase, inits, containers []corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{Status: corev1.PodStatus{Phase: phase, InitContainerStatuses: inits, ContainerStatuses: containers}}
}

func TestSiteBuildStatus(t *testing.T) {
	failedJob := func(reason string) *batchv1.Job {
		return &batchv1.Job{Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{
			{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: reason}}}}
	}
	cases := []struct {
		name    string
		job     *batchv1.Job
		pod     *corev1.Pod
		phase   sitebuilds.RunPhase
		failure sitebuilds.FailureKind
		code    int32
	}{
		{name: "no pod yet", job: &batchv1.Job{}, phase: sitebuilds.RunPending},
		{name: "cloning", job: &batchv1.Job{}, pod: buildPod(corev1.PodPending, []corev1.ContainerStatus{
			terminated("prepare-workdir", "Completed", 0), terminated("wait-for-fence", "Completed", 0), running("clone", false)}, nil),
			phase: sitebuilds.RunRunning},
		{name: "collect packing", job: &batchv1.Job{}, pod: buildPod(corev1.PodRunning, passedInits(), []corev1.ContainerStatus{running("collect", false)}),
			phase: sitebuilds.RunRunning},
		{name: "collect ready", job: &batchv1.Job{}, pod: buildPod(corev1.PodRunning, passedInits(), []corev1.ContainerStatus{running("collect", true)}),
			phase: sitebuilds.RunReady},
		{
			// Under gVisor only the killed process sees the kill: bash carries on and the step exits 0.
			name: "OOMKilled though the build exited 0", job: &batchv1.Job{},
			pod: buildPod(corev1.PodRunning, append(passedInits()[:3], terminated("build", "OOMKilled", 0)),
				[]corev1.ContainerStatus{running("collect", true)}),
			phase: sitebuilds.RunFailed, failure: sitebuilds.FailOutOfMemory,
		},
		{name: "OOMKilled with 137", job: &batchv1.Job{}, pod: buildPod(corev1.PodFailed,
			append(passedInits()[:3], terminated("build", "OOMKilled", 137)), nil), phase: sitebuilds.RunFailed, failure: sitebuilds.FailOutOfMemory},
		{name: "deadline", job: failedJob("DeadlineExceeded"), phase: sitebuilds.RunFailed, failure: sitebuilds.FailTimeout},
		{name: "evicted for disk", job: &batchv1.Job{}, pod: &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted",
			Message: "Usage of EmptyDir volume \"work\" exceeds the limit \"5Gi\"."}}, phase: sitebuilds.RunFailed, failure: sitebuilds.FailDisk},
		{name: "fence never came up", job: &batchv1.Job{}, pod: buildPod(corev1.PodFailed, []corev1.ContainerStatus{
			terminated("prepare-workdir", "Completed", 0), terminated("wait-for-fence", "Error", 1)}, nil),
			phase: sitebuilds.RunFailed, failure: sitebuilds.FailFence, code: 1},
		{name: "clone failed", job: &batchv1.Job{}, pod: buildPod(corev1.PodFailed, append(passedInits()[:2], terminated("clone", "Error", 1)), nil),
			phase: sitebuilds.RunFailed, failure: sitebuilds.FailClone, code: 1},
		{name: "install failed", job: &batchv1.Job{}, pod: buildPod(corev1.PodFailed, append(passedInits()[:3], terminated("build", "Error", 10)), nil),
			phase: sitebuilds.RunFailed, failure: sitebuilds.FailInstall, code: 10},
		{name: "build failed", job: &batchv1.Job{}, pod: buildPod(corev1.PodFailed, append(passedInits()[:3], terminated("build", "Error", 11)), nil),
			phase: sitebuilds.RunFailed, failure: sitebuilds.FailBuild, code: 11},
		{name: "no root dir", job: &batchv1.Job{}, pod: buildPod(corev1.PodFailed, append(passedInits()[:3], terminated("build", "Error", 12)), nil),
			phase: sitebuilds.RunFailed, failure: sitebuilds.FailRootDir, code: 12},
		{name: "killed otherwise", job: &batchv1.Job{}, pod: buildPod(corev1.PodFailed, append(passedInits()[:3], terminated("build", "Error", 137)), nil),
			phase: sitebuilds.RunFailed, failure: sitebuilds.FailBuild, code: 137},
		{name: "no output dir", job: &batchv1.Job{}, pod: buildPod(corev1.PodFailed, passedInits(), []corev1.ContainerStatus{terminated("collect", "Error", 3)}),
			phase: sitebuilds.RunFailed, failure: sitebuilds.FailNoOutput, code: 3},
		{name: "image cannot be pulled", job: &batchv1.Job{}, pod: buildPod(corev1.PodPending, []corev1.ContainerStatus{waiting("prepare-workdir", "ImagePullBackOff")}, nil),
			phase: sitebuilds.RunFailed, failure: sitebuilds.FailImage},
		{name: "job failed with no pod", job: failedJob("BackoffLimitExceeded"), phase: sitebuilds.RunFailed, failure: sitebuilds.FailPlatform},
		{name: "job marked failed before the step is read", job: failedJob("BackoffLimitExceeded"),
			pod:   buildPod(corev1.PodFailed, append(passedInits()[:3], terminated("build", "Error", 11)), nil),
			phase: sitebuilds.RunFailed, failure: sitebuilds.FailBuild, code: 11},
		{name: "deadline wins over the step it killed", job: failedJob("DeadlineExceeded"),
			pod:   buildPod(corev1.PodFailed, append(passedInits()[:3], terminated("build", "Error", 143)), nil),
			phase: sitebuilds.RunFailed, failure: sitebuilds.FailTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := interpretSiteBuild(tc.job, tc.pod)
			if got.Phase != tc.phase || got.Failure != tc.failure || got.ExitCode != tc.code {
				t.Fatalf("got %+v, want phase %d failure %q code %d", got, tc.phase, tc.failure, tc.code)
			}
			if got.Phase == sitebuilds.RunFailed && got.Detail == "" {
				t.Error("a failure must carry a detail for the build history")
			}
		})
	}
}

// The clone credential is dropped once the clone step has finished, while the build still runs.
func TestSiteBuildStatusReportsTheCloneDone(t *testing.T) {
	cloning := buildPod(corev1.PodPending, []corev1.ContainerStatus{
		terminated("prepare-workdir", "Completed", 0), terminated("wait-for-fence", "Completed", 0), running("clone", false)}, nil)
	if interpretSiteBuild(&batchv1.Job{}, cloning).CloneDone {
		t.Error("clone still running")
	}
	building := buildPod(corev1.PodPending, []corev1.ContainerStatus{
		terminated("prepare-workdir", "Completed", 0), terminated("wait-for-fence", "Completed", 0),
		terminated("clone", "Completed", 0), running("build", false)}, nil)
	if status := interpretSiteBuild(&batchv1.Job{}, building); !status.CloneDone || status.Phase != sitebuilds.RunRunning {
		t.Errorf("building: %+v", status)
	}
}
