package k8s

import (
	"context"
	"errors"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// applyAppsWithPullAuth deploys web and api, both pulling from ghcr.io with testPullAuth.
func applyAppsWithPullAuth(t *testing.T, c *Client) {
	t.Helper()
	for _, name := range []string{"web", "api"} {
		app := minimalApp()
		app.Name, app.ID = name, "app-"+name
		opts := testRenderOptions
		opts.PullAuth = testPullAuth
		workload, err := RenderAppWorkload(testNamespace, app, newResolver(), opts)
		if err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
		if err := c.ApplyAppWorkload(context.Background(), testNamespace, workload); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
}

var rotatedPullAuth = RegistryAuth{Registry: "ghcr.io", Username: "octocat", Password: "rotated"}

// A rotated credential reaches every running app's pull secret in place: no redeploy, nothing else touched.
func TestRefreshRegistryPullSecrets_RewritesEveryAppsSecretInPlace(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	applyAppsWithPullAuth(t, c)
	secrets := c.clientset.CoreV1().Secrets(testNamespace)
	other := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "other-pull", Namespace: testNamespace,
		Labels: map[string]string{appRegistryLabel: registryLabelValue("quay.io"), "app.kubernetes.io/managed-by": appManagedByValue}},
		Data: map[string][]byte{"k": []byte("untouched")}}
	if _, err := secrets.Create(ctx, other, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	stale, err := c.RefreshRegistryPullSecrets(ctx, testNamespace, rotatedPullAuth)
	if err != nil || len(stale) != 0 {
		t.Fatalf("refresh: %v %v", stale, err)
	}
	for _, name := range []string{"web", "api"} {
		secret, err := secrets.Get(ctx, AppPullSecretName(name), metav1.GetOptions{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		entry := decodeDockerConfig(t, secret).Auths["ghcr.io"]
		if entry.Password != "rotated" || len(secret.OwnerReferences) != 1 || secret.Type != corev1.SecretTypeDockerConfigJson {
			t.Errorf("%s's pull secret = %+v owners %v", name, entry, secret.OwnerReferences)
		}
	}
	if kept, _ := secrets.Get(ctx, "other-pull", metav1.GetOptions{}); string(kept.Data["k"]) != "untouched" {
		t.Errorf("another registry's secret changed: %v", kept.Data)
	}
}

func TestRefreshRegistryPullSecrets_NamesTheAppsLeftOnTheOldPassword(t *testing.T) {
	c := newFakeClient()
	applyAppsWithPullAuth(t, c)
	c.clientset.(*fake.Clientset).PrependReactor("update", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		secret := action.(k8stesting.UpdateAction).GetObject().(*corev1.Secret)
		return secret.Name == AppPullSecretName("api"), nil, errors.New("conflict")
	})
	stale, err := c.RefreshRegistryPullSecrets(context.Background(), testNamespace, rotatedPullAuth)
	if err == nil || !slices.Equal(stale, []string{"api"}) {
		t.Fatalf("stale %v err %v", stale, err)
	}
}

func TestRefreshRegistryPullSecrets_ListFailureIsAnError(t *testing.T) {
	c := newFakeClient()
	c.clientset.(*fake.Clientset).PrependReactor("list", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})
	if _, err := c.RefreshRegistryPullSecrets(context.Background(), testNamespace, rotatedPullAuth); err == nil {
		t.Fatal("a list that fails must not read as every secret updated")
	}
}

func TestMockClient_RefreshRegistryPullSecrets(t *testing.T) {
	m := NewMockClient()
	if stale, err := m.RefreshRegistryPullSecrets(context.Background(), "ns1", rotatedPullAuth); err != nil || stale != nil ||
		len(m.PullSecretsRefreshed) != 1 || m.PullSecretsRefreshed[0] != rotatedPullAuth {
		t.Fatalf("refresh: %v %v %v", stale, err, m.PullSecretsRefreshed)
	}
	m.PullSecretsRefreshStale, m.PullSecretsRefreshErr = []string{"web"}, errors.New("boom")
	if stale, err := m.RefreshRegistryPullSecrets(context.Background(), "ns1", rotatedPullAuth); err == nil || stale[0] != "web" {
		t.Fatalf("want the scripted failure: %v %v", stale, err)
	}
}
