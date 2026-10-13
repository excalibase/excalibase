package dockerapps

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

var platformHosts = []string{"api.example.com", "studio.example.com", "files.example.com"}

func edgeAliases(h *harness) []string {
	h.t.Helper()
	inspect, err := h.engine.ContainerInspect(context.Background(), "edge01")
	if err != nil {
		h.t.Fatal(err)
	}
	endpoint := inspect.NetworkSettings.Networks[testNet]
	if endpoint == nil {
		return nil
	}
	return endpoint.Aliases
}

// EXC-608: an app calls the platform by its public names (https://api.<domain>); on its project network
// those names are the edge's, so the call reaches the edge whatever public DNS says.
func TestTheEdgeAnswersToThePlatformHostsOnAProjectNetwork(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.EdgeAliases = platformHosts })
	h.rollout(freeApp(), "dep-1")
	if got := edgeAliases(h); !slices.Equal(got, platformHosts) {
		t.Fatalf("edge aliases on the project network = %v, want %v", got, platformHosts)
	}
}

// With its own CA (localhost) the edge's certificates are trusted by nothing in an app: the app gets the
// CA's root and Node is pointed at it, so a server-side fetch of https://api.<domain> verifies.
func TestAnAppTrustsTheEdgesOwnCA(t *testing.T) {
	root := []byte("-----BEGIN CERTIFICATE-----\nedge-root\n-----END CERTIFICATE-----\n")
	h := newHarness(t, func(o *Options) {
		o.EdgeIssuesLocally = true
		o.EdgeCA = func(context.Context) ([]byte, error) { return root, nil }
	})
	h.rollout(freeApp(), "dep-1")
	app := h.engine.appContainers("app-01")[0]
	if got := string(app.files["/"+probeDir+"/edge-ca.crt"]); got != string(root) {
		t.Fatalf("edge CA in the app = %q", got)
	}
	if !slices.Contains(app.config.Env, "NODE_EXTRA_CA_CERTS=/"+probeDir+"/edge-ca.crt") {
		t.Fatalf("env = %v", app.config.Env)
	}
}

// The app's own NODE_EXTRA_CA_CERTS is its operator's choice and is kept.
func TestAnAppsOwnExtraCAsAreKept(t *testing.T) {
	h := newHarness(t, func(o *Options) {
		o.EdgeIssuesLocally = true
		o.EdgeCA = func(context.Context) ([]byte, error) { return []byte("root"), nil }
	})
	app := freeApp()
	app.Env = append(app.Env, apphost.EnvVar{Name: "NODE_EXTRA_CA_CERTS", Kind: apphost.KindLiteral, Value: literal("/certs/mine.pem")})
	h.rollout(app, "dep-1")
	env := h.engine.appContainers("app-01")[0].config.Env
	if !slices.Contains(env, "NODE_EXTRA_CA_CERTS=/certs/mine.pem") || slices.Contains(env, "NODE_EXTRA_CA_CERTS=/"+probeDir+"/edge-ca.crt") {
		t.Fatalf("env = %v", env)
	}
}

// No CA to hand the app is a failed deploy, never an app that cannot reach the platform.
func TestADeployFailsWhenTheEdgesCACannotBeRead(t *testing.T) {
	h := newHarness(t, func(o *Options) {
		o.EdgeIssuesLocally = true
		o.EdgeCA = func(context.Context) ([]byte, error) { return nil, errors.New("edge not up") }
	})
	workload, err := k8s.RenderAppWorkload(testDB, freeApp(), stubResolver{}, renderOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.rt.ApplyAppWorkload(context.Background(), testDB, workload); err == nil || !strings.Contains(err.Error(), "edge not up") {
		t.Fatalf("apply = %v, want the CA failure", err)
	}
}
