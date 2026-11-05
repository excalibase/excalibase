package k8s

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/excalibase/provisioning-poc/internal/appbuilds"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

func terminatedWith(name, reason, message string, code int32) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
		Reason: reason, Message: message, ExitCode: code}}}
}

// runsc sometimes refuses to start a step; that is the sandbox's fault, not the build's, and is named so it can be retried.
func TestBuildStatus_NamesASandboxThatDidNotStart(t *testing.T) {
	stopped := "failed to create containerd task: starting container: cannot start container \"abc\" in state stopped"
	for name, step := range map[string]corev1.ContainerStatus{
		"containerd's reason":    terminatedWith("build", "StartError", "", 128),
		"an app build's step":    terminatedWith("buildkit", "StartError", stopped, 128),
		"the clone step refused": terminatedWith("clone", "StartError", "", 128),
	} {
		pod := buildPod(corev1.PodPending, []corev1.ContainerStatus{terminated("prepare-workdir", "Completed", 0), step}, nil)
		for kind, kindOf := range map[string]func(string, int32) sitebuilds.FailureKind{"site": stepFailureKind, "app": appStepFailureKind} {
			status := interpretBuild(&batchv1.Job{}, pod, kindOf)
			if status.Phase != sitebuilds.RunFailed || status.Failure != sitebuilds.FailPlatform || !status.StartFailed || status.ExitCode != 128 {
				t.Errorf("%s (%s build): %+v", name, kind, status)
			}
		}
	}
	plain := buildPod(corev1.PodPending, []corev1.ContainerStatus{terminatedWith("build", "Error", "", 128)}, nil)
	if status := interpretBuild(&batchv1.Job{}, plain, stepFailureKind); status.StartFailed || status.Failure != sitebuilds.FailBuild {
		t.Errorf("a build that itself exited 128 is the build's failure: %+v", status)
	}
	// The termination message is the build's own to write, so it never earns a retry.
	spoofed := buildPod(corev1.PodPending, []corev1.ContainerStatus{terminatedWith("build", "Error", stopped, 128)}, nil)
	if status := interpretBuild(&batchv1.Job{}, spoofed, stepFailureKind); status.StartFailed || status.Failure != sitebuilds.FailBuild {
		t.Errorf("a build that printed the sandbox's message is still the build's failure: %+v", status)
	}
}

func TestBuildJobs_ForgetCloneCredentialDeletesOnlyTheGitSecret(t *testing.T) {
	runner, client := newAppBuildRunner(t)
	ctx := context.Background()
	spec := testAppRunSpec(appbuilds.BuilderDockerfile)
	if err := runner.Start(ctx, spec); err != nil {
		t.Fatal(err)
	}
	secrets := client.clientset.CoreV1().Secrets("app-builds")
	if err := runner.ForgetCloneCredential(ctx, spec.BuildID); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Get(ctx, appBuildName(spec.BuildID)+"-git", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("the clone secret outlived the clone: %v", err)
	}
	if _, err := secrets.Get(ctx, appBuildName(spec.BuildID)+"-secrets", metav1.GetOptions{}); err != nil {
		t.Errorf("the build's own secrets went too: %v", err)
	}
	if err := runner.ForgetCloneCredential(ctx, spec.BuildID); err != nil {
		t.Errorf("forgetting twice: %v", err)
	}
}
