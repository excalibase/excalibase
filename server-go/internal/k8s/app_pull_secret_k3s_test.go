//go:build live

package k8s

import (
	"bufio"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	liveRegistry        = "localhost:5000"
	liveRegistryUser    = "puller"
	liveRegistryPass    = "pull-secret-live"
	livePrivateImage    = liveRegistry + "/private/web:1"
	liveRegistryNS      = "registry"
	liveRegistryImage   = "registry:2.8.3"
	liveCraneImage      = "gcr.io/go-containerregistry/crane:debug"
	livePublicSourceRef = "nginxinc/nginx-unprivileged:1.27"
)

// Run with: go test ./internal/k8s/ -tags=live -run TestK3sPrivateRegistryPull -v -timeout 30m
func TestK3sPrivateRegistryPull(t *testing.T) {
	g := startGVisorK3s(t, gvisorPlatformSystrap)
	g.startPrivateRegistry(t)
	g.pushPrivateImage(t)
	owner := g.createNamespace(t, "org1-proj-owner")
	intruder := g.createNamespace(t, "org2-proj-intruder")

	t.Run("without the credential the pull is refused", func(t *testing.T) {
		err := g.deployWithAuth(t, owner, "anon", nil, 2*time.Minute)
		if !errors.Is(err, ErrAppRollout) {
			t.Fatalf("want a pull failure, got %v", err)
		}
		t.Logf("anonymous pull: %v", err)
	})
	t.Run("with the project's credential it rolls out", func(t *testing.T) {
		auth := &RegistryAuth{Registry: liveRegistry, Username: liveRegistryUser, Password: liveRegistryPass}
		if err := g.deployWithAuth(t, owner, "private", auth, 3*time.Minute); err != nil {
			g.logPullDiagnostics(t, owner)
			t.Fatalf("want success, got %v", err)
		}
	})
	t.Run("another tenant cannot run the image the node now caches", func(t *testing.T) {
		err := g.deployWithAuth(t, intruder, "thief", nil, 2*time.Minute)
		if !errors.Is(err, ErrAppRollout) {
			t.Fatalf("a cached private image ran without its credential: %v", err)
		}
		t.Logf("cross-tenant pull: %v", err)
	})
}

func (g *gvisorCluster) deployWithAuth(t *testing.T, namespace, name string, auth *RegistryAuth, timeout time.Duration) error {
	t.Helper()
	app := liveApp(name, livePrivateImage, 8080)
	app.ID = "app-" + namespace + "-" + name
	workload, err := RenderAppWorkload(namespace, app, nil, AppRenderOptions{RuntimeClass: gvisorRuntimeClass, Route: liveRoute, DeployID: liveDeployID, PullAuth: auth})
	if err != nil {
		t.Fatalf("render %s: %v", name, err)
	}
	ctx := context.Background()
	if err := g.client.ApplyAppWorkload(ctx, namespace, workload); err != nil {
		t.Fatalf("apply %s: %v", name, err)
	}
	return g.client.WaitForAppRollout(ctx, namespace, AppObjectName(name), liveDeployID, timeout)
}

// logPullDiagnostics prints what a refused credentialed pull needs explaining
// with: whether the pull Secret existed, the pod's events, and what the kubelet
// said about the pull secret. The one CI failure of this test (PR 423) answered
// "no basic auth credentials" for a pod that carried the Secret, and nothing
// kept to say whether the kubelet had read it.
func (g *gvisorCluster) logPullDiagnostics(t *testing.T, namespace string) {
	t.Helper()
	ctx := context.Background()
	secrets, err := g.clientset.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Logf("diagnostics: list secrets: %v", err)
	}
	for _, secret := range secrets.Items {
		t.Logf("diagnostics: secret %s type=%s created=%s", secret.Name, secret.Type, secret.CreationTimestamp.Format(time.RFC3339Nano))
	}
	events, err := g.clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Logf("diagnostics: list events: %v", err)
	}
	for _, event := range events.Items {
		// Events of the newer API leave LastTimestamp zero and carry EventTime.
		when := event.LastTimestamp.Time
		if when.IsZero() {
			when = event.EventTime.Time
		}
		t.Logf("diagnostics: event %s %s/%s %s: %s", when.Format(time.RFC3339Nano), event.InvolvedObject.Kind, event.InvolvedObject.Name, event.Reason, event.Message)
	}
	reader, err := g.container.Logs(ctx)
	if err != nil {
		t.Logf("diagnostics: k3s logs: %v", err)
		return
	}
	defer reader.Close()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "pull secret") || strings.Contains(line, "app-private") || strings.Contains(line, "failed to sync secret") {
			t.Logf("diagnostics: k3s: %s", line)
		}
	}
}

// startPrivateRegistry runs a registry demanding basic auth on the node's
// loopback, which containerd reaches over plain HTTP.
func (g *gvisorCluster) startPrivateRegistry(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	g.createNamespace(t, liveRegistryNS)
	hash, err := bcrypt.GenerateFromPassword([]byte(liveRegistryPass), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "htpasswd", Namespace: liveRegistryNS},
		Data:       map[string][]byte{"htpasswd": []byte(liveRegistryUser + ":" + string(hash) + "\n")},
	}
	if _, err := g.clientset.CoreV1().Secrets(liveRegistryNS).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		t.Fatalf("htpasswd secret: %v", err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "registry", Namespace: liveRegistryNS},
		Spec: corev1.PodSpec{
			HostNetwork: true,
			Containers: []corev1.Container{{
				Name: "registry", Image: liveRegistryImage,
				Env: []corev1.EnvVar{
					{Name: "REGISTRY_AUTH", Value: "htpasswd"},
					{Name: "REGISTRY_AUTH_HTPASSWD_REALM", Value: "live"},
					{Name: "REGISTRY_AUTH_HTPASSWD_PATH", Value: "/auth/htpasswd"},
				},
				VolumeMounts: []corev1.VolumeMount{{Name: "auth", MountPath: "/auth"}},
			}},
			Volumes: []corev1.Volume{{Name: "auth", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "htpasswd"}}}},
		},
	}
	if _, err := g.clientset.CoreV1().Pods(liveRegistryNS).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("registry pod: %v", err)
	}
	g.waitPodReady(t, liveRegistryNS, "registry")
}

// pushPrivateImage copies a public image into the private registry.
func (g *gvisorCluster) pushPrivateImage(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	script := "crane auth login " + liveRegistry + " -u " + liveRegistryUser + " -p " + liveRegistryPass +
		" && crane copy --insecure --platform linux/amd64 " + livePublicSourceRef + " " + livePrivateImage
	backoff := int32(0)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "push", Namespace: liveRegistryNS},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				HostNetwork:   true,
				RestartPolicy: corev1.RestartPolicyNever,
				Containers: []corev1.Container{{
					Name: "crane", Image: liveCraneImage, Command: []string{"sh", "-c", script},
				}},
			}},
		},
	}
	if _, err := g.clientset.BatchV1().Jobs(liveRegistryNS).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		t.Fatalf("push job: %v", err)
	}
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		got, err := g.clientset.BatchV1().Jobs(liveRegistryNS).Get(ctx, "push", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if got.Status.Failed > 0 {
			return false, errors.New("the push job failed")
		}
		return got.Status.Succeeded > 0, nil
	})
	if err != nil {
		t.Fatalf("push %s: %v", livePrivateImage, err)
	}
}

func (g *gvisorCluster) waitPodReady(t *testing.T, namespace, name string) {
	t.Helper()
	err := wait.PollUntilContextTimeout(context.Background(), 2*time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		pod, err := g.clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("pod %s/%s not ready: %v", namespace, name, err)
	}
}
