package k8s

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var testPullAuth = &RegistryAuth{Registry: "ghcr.io", Username: "octocat", Password: "ghp_secret"}

func renderWithPullAuth(t *testing.T, auth *RegistryAuth) *AppWorkload {
	t.Helper()
	opts := testRenderOptions
	opts.PullAuth = auth
	workload, err := RenderAppWorkload(testNamespace, minimalApp(), newResolver(), opts)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return workload
}

type dockerConfig struct {
	Auths map[string]struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Auth     string `json:"auth"`
	} `json:"auths"`
}

func decodeDockerConfig(t *testing.T, secret *corev1.Secret) dockerConfig {
	t.Helper()
	var config dockerConfig
	if err := json.Unmarshal(secret.Data[corev1.DockerConfigJsonKey], &config); err != nil {
		t.Fatalf("decode %s: %v", corev1.DockerConfigJsonKey, err)
	}
	return config
}

func TestRenderAppWorkload_PullSecretCarriesOnlyTheImagesRegistry(t *testing.T) {
	workload := renderWithPullAuth(t, testPullAuth)
	secret := workload.PullSecret
	if secret == nil {
		t.Fatal("no pull secret rendered")
	}
	if secret.Type != corev1.SecretTypeDockerConfigJson || secret.Name != AppPullSecretName("web") || secret.Namespace != testNamespace {
		t.Fatalf("secret = %s/%s type %s", secret.Namespace, secret.Name, secret.Type)
	}
	config := decodeDockerConfig(t, secret)
	entry, ok := config.Auths["ghcr.io"]
	if !ok || len(config.Auths) != 1 {
		t.Fatalf("auths = %v, want ghcr.io only", config.Auths)
	}
	if entry.Username != "octocat" || entry.Password != "ghp_secret" ||
		entry.Auth != base64.StdEncoding.EncodeToString([]byte("octocat:ghp_secret")) {
		t.Fatalf("entry = %+v", entry)
	}
	if secret.Labels["excalibase.io/app"] != minimalApp().ID || secret.Labels[appRegistryLabel] != registryLabelValue("ghcr.io") {
		t.Fatalf("labels = %v", secret.Labels)
	}
	pullSecrets := workload.Deployment.Spec.Template.Spec.ImagePullSecrets
	if len(pullSecrets) != 1 || pullSecrets[0].Name != secret.Name {
		t.Fatalf("imagePullSecrets = %v", pullSecrets)
	}
}

func TestRenderAppWorkload_DockerHubUsesItsLegacyKeyToo(t *testing.T) {
	opts := testRenderOptions
	opts.PullAuth = &RegistryAuth{Registry: "docker.io", Username: "u", Password: "p"}
	app := minimalApp()
	app.Image = "acme/web:1"
	workload, err := RenderAppWorkload(testNamespace, app, newResolver(), opts)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	config := decodeDockerConfig(t, workload.PullSecret)
	for _, key := range []string{"https://index.docker.io/v1/", "docker.io"} {
		if _, ok := config.Auths[key]; !ok {
			t.Errorf("missing %s in %v", key, config.Auths)
		}
	}
}

func TestRenderAppWorkload_RefusesCredentialsForAnotherRegistry(t *testing.T) {
	opts := testRenderOptions
	opts.PullAuth = &RegistryAuth{Registry: "registry.evil.example", Username: "u", Password: "p"}
	if _, err := RenderAppWorkload(testNamespace, minimalApp(), newResolver(), opts); err == nil {
		t.Fatal("credentials must only ever go to the registry the image is pulled from")
	}
}

func TestRenderAppWorkload_NoCredentialsNoPullSecret(t *testing.T) {
	workload := renderWithPullAuth(t, nil)
	if workload.PullSecret != nil || len(workload.Deployment.Spec.Template.Spec.ImagePullSecrets) != 0 {
		t.Fatal("a public image needs no pull secret")
	}
}

func TestApplyAppWorkload_AppliesOwnsAndRemovesThePullSecret(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	if err := c.ApplyAppWorkload(ctx, testNamespace, renderWithPullAuth(t, testPullAuth)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	secrets := c.clientset.CoreV1().Secrets(testNamespace)
	stored, err := secrets.Get(ctx, AppPullSecretName("web"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("pull secret: %v", err)
	}
	if len(stored.OwnerReferences) != 1 || stored.OwnerReferences[0].Kind != "Deployment" {
		t.Fatalf("owner = %+v", stored.OwnerReferences)
	}

	rotated := &RegistryAuth{Registry: "ghcr.io", Username: "octocat", Password: "ghp_rotated"}
	if err := c.ApplyAppWorkload(ctx, testNamespace, renderWithPullAuth(t, rotated)); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	stored, _ = secrets.Get(ctx, AppPullSecretName("web"), metav1.GetOptions{})
	if decodeDockerConfig(t, stored).Auths["ghcr.io"].Password != "ghp_rotated" {
		t.Fatal("a redeploy must carry the rotated credential")
	}

	if err := c.ApplyAppWorkload(ctx, testNamespace, renderWithPullAuth(t, nil)); err != nil {
		t.Fatalf("third apply: %v", err)
	}
	if _, err := secrets.Get(ctx, AppPullSecretName("web"), metav1.GetOptions{}); err == nil {
		t.Fatal("a credential the app no longer uses must not stay in the cluster")
	}
}

func TestDeleteRegistryPullSecrets_RemovesOnlyThatRegistrysSecrets(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	if err := c.ApplyAppWorkload(ctx, testNamespace, renderWithPullAuth(t, testPullAuth)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	other := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "other-pull", Namespace: testNamespace,
		Labels: map[string]string{appRegistryLabel: registryLabelValue("quay.io"), "app.kubernetes.io/managed-by": appManagedByValue}}}
	if _, err := c.clientset.CoreV1().Secrets(testNamespace).Create(ctx, other, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := c.DeleteRegistryPullSecrets(ctx, testNamespace, "ghcr.io"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := c.clientset.CoreV1().Secrets(testNamespace).Get(ctx, AppPullSecretName("web"), metav1.GetOptions{}); err == nil {
		t.Error("the revoked registry's pull secret must go")
	}
	if _, err := c.clientset.CoreV1().Secrets(testNamespace).Get(ctx, "other-pull", metav1.GetOptions{}); err != nil {
		t.Errorf("another registry's secret must stay: %v", err)
	}
}
