//go:build live

package k8s

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

const liveSecretValue = "sk_live_k3s_env_5b7"

// staticResolver hands out fixed values: the vault and project lookups are
// the service's and are covered there; this proves the cluster side.
type staticResolver struct{}

func (staticResolver) ResolveReference(string, apphost.ResolvedReference) (string, error) {
	return "postgresql://owner:pw@db-rw.org1-proj2.svc.cluster.local:5432/app?sslmode=require", nil
}

func (staticResolver) ResolveSecret(string, apphost.SecretRef) (string, error) {
	return liveSecretValue, nil
}

// Run with: go test ./internal/k8s/ -tags=live -run TestK3sAppEnvSecret -v -timeout 30m
func TestK3sAppEnvSecret(t *testing.T) {
	g := startGVisorK3s(t, gvisorPlatformSystrap)
	namespace := g.createNamespace(t, "org1-proj2")
	app := liveApp("envapp", "nginxinc/nginx-unprivileged:1.27", 8080)
	secret := apphost.AppSecretRef(app.ProjectID, app.ID, "API_KEY")
	app.Env = []apphost.EnvVar{{Name: "API_KEY", Kind: apphost.KindSecret, Secret: &secret}}

	workload, err := RenderAppWorkload(namespace, app, staticResolver{},
		AppRenderOptions{RuntimeClass: gvisorRuntimeClass, Route: liveRoute, EnvRevision: "1"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	ctx := context.Background()
	if err := g.client.ApplyAppWorkload(ctx, namespace, workload); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := g.client.WaitForAppRollout(ctx, namespace, AppObjectName(app.Name), 3*time.Minute); err != nil {
		t.Fatalf("rollout: %v", err)
	}

	if got := strings.TrimSpace(g.exec(t, g.appPod(t, namespace, app.Name), "printenv API_KEY")); got != liveSecretValue {
		t.Fatalf("the container reads API_KEY = %q, want the stored value", got)
	}
	deployment, err := g.clientset.AppsV1().Deployments(namespace).Get(ctx, AppObjectName(app.Name), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read deployment: %v", err)
	}
	encoded, _ := json.Marshal(deployment)
	if strings.Contains(string(encoded), liveSecretValue) {
		t.Fatal("the live Deployment carries the secret value")
	}
	stored, err := g.clientset.CoreV1().Secrets(namespace).Get(ctx, AppEnvSecretName(app.Name), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read env secret: %v", err)
	}
	if len(stored.OwnerReferences) != 1 || stored.OwnerReferences[0].UID != deployment.UID {
		t.Fatalf("env secret owner = %+v, want the Deployment %s", stored.OwnerReferences, deployment.UID)
	}
}
