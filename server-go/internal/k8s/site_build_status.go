package k8s

import (
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

const (
	reasonOOMKilled = "OOMKilled"
	// reasonStartError is the kubelet's reason for a container whose task never started.
	reasonStartError = "StartError"
)

// Exit codes build.sh and collect.sh use to name the failing step.
const (
	exitInstall  = 10
	exitBuild    = 11
	exitRootDir  = 12
	exitNoOutput = 3
)

// interpretSiteBuild reads the Job and its pod. Kills are read from each
// step's terminated reason first: under gVisor an out-of-memory kill reaches
// only the killed process, so the step itself may still exit 0.
func interpretSiteBuild(job *batchv1.Job, pod *corev1.Pod) sitebuilds.RunStatus {
	return interpretBuild(job, pod, stepFailureKind)
}

// interpretBuild reads any build Job the same way; kindOf names a failing step's kind.
func interpretBuild(job *batchv1.Job, pod *corev1.Pod, kindOf func(step string, code int32) sitebuilds.FailureKind) sitebuilds.RunStatus {
	if pod != nil {
		if status, done := podKills(pod); done {
			return status
		}
	}
	if status, failed := jobFailure(job); failed {
		return status
	}
	if pod == nil {
		return sitebuilds.RunStatus{Phase: sitebuilds.RunPending}
	}
	if status, failed := stepFailure(pod, kindOf); failed {
		return status
	}
	for _, container := range pod.Status.ContainerStatuses {
		if container.Name == siteBuildCollect && container.Ready {
			return sitebuilds.RunStatus{Phase: sitebuilds.RunReady}
		}
	}
	if pod.Status.Phase == corev1.PodFailed {
		return failed(sitebuilds.FailPlatform, 0, "the build pod failed: "+podMessage(pod))
	}
	if pod.Status.Phase == corev1.PodPending && len(pod.Status.InitContainerStatuses) == 0 {
		return sitebuilds.RunStatus{Phase: sitebuilds.RunPending}
	}
	return sitebuilds.RunStatus{Phase: sitebuilds.RunRunning, CloneDone: cloneDone(pod)}
}

func cloneDone(pod *corev1.Pod) bool {
	for _, status := range pod.Status.InitContainerStatuses {
		if status.Name == siteBuildClone && status.State.Terminated != nil {
			return true
		}
	}
	return false
}

func failed(kind sitebuilds.FailureKind, code int32, detail string) sitebuilds.RunStatus {
	return sitebuilds.RunStatus{Phase: sitebuilds.RunFailed, Failure: kind, ExitCode: code, Detail: detail}
}

func podMessage(pod *corev1.Pod) string {
	if pod.Status.Message != "" {
		return pod.Status.Message
	}
	return string(pod.Status.Phase)
}

// podKills is what the kubelet or the kernel stopped: memory, disk, or an image that never pulled.
func podKills(pod *corev1.Pod) (sitebuilds.RunStatus, bool) {
	for _, status := range allStatuses(pod) {
		if terminated := status.State.Terminated; terminated != nil && terminated.Reason == reasonOOMKilled {
			return failed(sitebuilds.FailOutOfMemory, 0, fmt.Sprintf("the %s step ran out of memory", status.Name)), true
		}
		if waiting := status.State.Waiting; waiting != nil && badImageReasons[waiting.Reason] {
			return failed(sitebuilds.FailImage, 0, "the build image could not be pulled ("+waiting.Reason+")"), true
		}
	}
	if pod.Status.Reason == "Evicted" {
		return failed(sitebuilds.FailDisk, 0, "the build was stopped for using more disk than allowed: "+pod.Status.Message), true
	}
	return sitebuilds.RunStatus{}, false
}

func allStatuses(pod *corev1.Pod) []corev1.ContainerStatus {
	return append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...)
}

func jobFailure(job *batchv1.Job) (sitebuilds.RunStatus, bool) {
	for _, condition := range job.Status.Conditions {
		if condition.Type != batchv1.JobFailed || condition.Status != corev1.ConditionTrue {
			continue
		}
		if condition.Reason == batchv1.JobReasonDeadlineExceeded {
			return failed(sitebuilds.FailTimeout, 0, "the build ran past its time limit"), true
		}
		return failed(sitebuilds.FailPlatform, 0, "the build job failed: "+condition.Reason), true
	}
	return sitebuilds.RunStatus{}, false
}

// stepFailure names the first step that exited non-zero.
func stepFailure(pod *corev1.Pod, kindOf func(step string, code int32) sitebuilds.FailureKind) (sitebuilds.RunStatus, bool) {
	for _, status := range allStatuses(pod) {
		terminated := status.State.Terminated
		if terminated == nil || terminated.ExitCode == 0 {
			continue
		}
		code := terminated.ExitCode
		// Only the kubelet's reason counts: the termination message is the build's own to write.
		if terminated.Reason == reasonStartError {
			notStarted := failed(sitebuilds.FailPlatform, code, fmt.Sprintf("the sandbox could not start the %s step", status.Name))
			notStarted.StartFailed = true
			return notStarted, true
		}
		return failed(kindOf(status.Name, code), code, fmt.Sprintf("the %s step exited %d", status.Name, code)), true
	}
	return sitebuilds.RunStatus{}, false
}

func stepFailureKind(step string, code int32) sitebuilds.FailureKind {
	switch step {
	case siteBuildFence:
		return sitebuilds.FailFence
	case siteBuildClone:
		return sitebuilds.FailClone
	case siteBuildBuild:
		switch code {
		case exitInstall:
			return sitebuilds.FailInstall
		case exitRootDir:
			return sitebuilds.FailRootDir
		default:
			return sitebuilds.FailBuild
		}
	case siteBuildCollect:
		if code == exitNoOutput {
			return sitebuilds.FailNoOutput
		}
	}
	return sitebuilds.FailPlatform
}
