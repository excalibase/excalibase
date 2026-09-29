package k8s

import (
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// An argument naming a secret variable reaches the container as $(NAME): the
// cluster fills it in from the env, so the Deployment never holds the value.
func TestRenderAppArgsNameTheSecretWithoutCarryingIt(t *testing.T) {
	app := fullApp()
	app.Args = []string{"--requirepass", "$(STRIPE_KEY)"}
	workload := mustRender(t, app, newResolver())
	container := workload.Deployment.Spec.Template.Spec.Containers[0]
	if !slices.Equal(container.Args, app.Args) {
		t.Fatalf("container args = %v, want %v", container.Args, app.Args)
	}
	if len(container.Command) != 0 {
		t.Fatalf("the image's entrypoint must stay: command = %v", container.Command)
	}
	encoded, err := yaml.Marshal(workload.Deployment)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), testStripeKey) {
		t.Fatalf("the Deployment carries the secret value:\n%s", encoded)
	}
	app.Args[0] = "--changed"
	if container.Args[0] != "--requirepass" {
		t.Fatal("the rendered container shares the app's args slice")
	}
}

func TestRenderAppWithoutArgsKeepsTheImageCommand(t *testing.T) {
	workload := mustRender(t, minimalApp(), newResolver())
	if args := workload.Deployment.Spec.Template.Spec.Containers[0].Args; args != nil {
		t.Fatalf("args = %v, want none", args)
	}
}
