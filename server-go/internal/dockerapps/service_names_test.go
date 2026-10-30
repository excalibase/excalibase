package dockerapps

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/engineproxy"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// Apps reach each other by name as on Kubernetes: one alias under the
// service domain, which the search list completes from the bare name.
func TestApplyNamesTheAppOnItsProjectNetwork(t *testing.T) {
	h := newHarness(t, nil)
	h.deploy(freeApp(), "dep-1")
	c := h.engine.appContainers("app-01")[0]
	if want := []string{"web." + engineproxy.AppServiceDomain}; !slices.Equal(c.aliases[testNet], want) {
		t.Fatalf("aliases %v, want %v", c.aliases[testNet], want)
	}
	if !slices.Equal(c.host.DNSSearch, []string{engineproxy.AppServiceDomain}) || !slices.Equal(c.host.DNSOptions, []string{"ndots:1"}) {
		t.Fatalf("dns search %v options %v", c.host.DNSSearch, c.host.DNSOptions)
	}
}

// An app name that would answer for one of the platform's hosts is refused.
func TestApplyRefusesANameThatIsAPlatformHost(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.EdgeAliases = []string{"api." + engineproxy.AppServiceDomain} })
	app := freeApp()
	app.Name = "api"
	workload, err := k8s.RenderAppWorkload(testDB, app, stubResolver{}, renderOptions())
	if err != nil {
		t.Fatal(err)
	}
	err = h.rt.ApplyAppWorkload(context.Background(), testDB, workload)
	if err == nil || !strings.Contains(err.Error(), "platform") {
		t.Fatalf("want a refusal, got %v", err)
	}
	if len(h.engine.appContainers("app-01")) != 0 {
		t.Fatal("no container is made")
	}
}

// Arguments read the container's variables as the kubelet expands them.
func TestSpecFromWorkloadExpandsArgsAsKubernetesDoes(t *testing.T) {
	app := testApp()
	app.Env = []apphost.EnvVar{
		{Name: "PW", Kind: apphost.KindLiteral, Value: literal("s3cret")},
		{Name: "API_KEY", Kind: apphost.KindSecret, Secret: func() *apphost.SecretRef {
			ref := apphost.AppSecretRef(app.ProjectID, app.ID, "API_KEY")
			return &ref
		}()},
	}
	app.Args = []string{"--requirepass", "$(PW)", "$$(PW)", "$(MISSING)", "a$(PW)b$(API_KEY)", "$(", "$$", "$", "50$", "$x"}
	spec, err := specFromWorkload(render(t, app, renderOptions()))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--requirepass", "s3cret", "$(PW)", "$(MISSING)", "as3cretbsk_live_secret", "$(", "$", "$", "50$", "$x"}
	if !slices.Equal(spec.Args, want) {
		t.Fatalf("args %q, want %q", spec.Args, want)
	}
}
