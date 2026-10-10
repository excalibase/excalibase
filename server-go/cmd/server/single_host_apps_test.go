package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

func TestProjectScopesMapADatabaseContainerToItsProject(t *testing.T) {
	instances := fakestore.NewInstances()
	instances.Items["proj-a"] = &domain.DatabaseInstance{ProjectID: "proj-a", Namespace: "container-a"}
	instances.Items["proj-b"] = &domain.DatabaseInstance{ProjectID: "proj-b", Namespace: "container-b"}
	instances.Items["proj-c"] = &domain.DatabaseInstance{ProjectID: "proj-c"}
	scopes := projectScopes{instances: instances}
	if project, err := scopes.ProjectOf("container-b"); err != nil || project != "proj-b" {
		t.Fatalf("ProjectOf = %q (%v)", project, err)
	}
	for _, unknown := range []string{"", "container-z"} {
		if _, err := scopes.ProjectOf(unknown); err == nil {
			t.Errorf("%q resolved to a project", unknown)
		}
	}
	if namespace, err := scopes.NamespaceOf("proj-a"); err != nil || namespace != "container-a" {
		t.Fatalf("NamespaceOf = %q (%v)", namespace, err)
	}
	if _, err := scopes.NamespaceOf("proj-c"); err == nil {
		t.Error("a project without a database container has a scope")
	}
}

func TestProjectScopesRefuseAContainerTwoProjectsName(t *testing.T) {
	instances := fakestore.NewInstances()
	instances.Items["proj-a"] = &domain.DatabaseInstance{ProjectID: "proj-a", Namespace: "same"}
	instances.Items["proj-b"] = &domain.DatabaseInstance{ProjectID: "proj-b", Namespace: "same"}
	if _, err := (projectScopes{instances: instances}).ProjectOf("same"); err == nil {
		t.Fatal("an ambiguous container resolved to a project")
	}
}

func TestSingleHostAppRuntimeOnlyInDockerModeWithHosting(t *testing.T) {
	for name, cfg := range map[string]config.AppConfig{
		"kubernetes":       {ProvisionerMode: "k8s", AppHostingEnabled: true},
		"docker, apps off": {ProvisionerMode: "docker"},
	} {
		runtime, err := buildSingleHostAppRuntime(context.Background(), cfg, nil, fakestore.NewInstances())
		if err != nil || runtime != nil {
			t.Errorf("%s: runtime %v err %v", name, runtime, err)
		}
	}
	cfg := config.AppConfig{ProvisionerMode: "docker", AppHostingEnabled: true}
	if _, err := buildSingleHostAppRuntime(context.Background(), cfg, nil, fakestore.NewInstances()); err == nil {
		t.Fatal("apps on a single host started without an engine")
	}
}

func TestAppRenderOptionsPerMode(t *testing.T) {
	docker := config.AppConfig{ProvisionerMode: "docker", AppDomain: "apps.example.com"}
	docker.SingleHostApps.SandboxRuntime = "runsc"
	if opts := appRenderOptions(docker); opts.Route.Domain != "apps.example.com" || opts.RuntimeClass != "runsc" || !opts.Route.WildcardTLS {
		t.Fatalf("docker render options %+v", opts)
	}
	docker.SingleHostApps.SandboxRuntime = ""
	if opts := appRenderOptions(docker); opts.RuntimeClass == "" {
		t.Fatal("an opted-out sandbox left the renderer without a runtime class")
	}
	cluster := config.AppConfig{ProvisionerMode: "k8s", AppRuntimeClass: "gvisor", AppDomain: "apps.example.com", AppIngressClass: "haproxy"}
	if opts := appRenderOptions(cluster); opts.RuntimeClass != "gvisor" || opts.Route.IngressClass != "haproxy" {
		t.Fatalf("kubernetes render options %+v", opts)
	}
}

func TestVerifyAppRuntime_DockerModeIsCheckedByTheSingleHostRuntime(t *testing.T) {
	cfg := config.AppConfig{ProvisionerMode: "docker", AppHostingEnabled: true}
	if err := verifyAppRuntime(context.Background(), cfg, nil); err != nil {
		t.Fatalf("docker mode asked for a RuntimeClass: %v", err)
	}
	var kube k8s.KubeClient
	if err := verifyAppRuntime(context.Background(), config.AppConfig{ProvisionerMode: "k8s", AppHostingEnabled: true}, kube); err == nil {
		t.Fatal("kubernetes mode without a cluster passed")
	}
}

func TestSingleHostAppsWiredAtBoot(t *testing.T) {
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"buildSingleHostAppRuntime(", "appRuntimeFor(", "appRenderOptions(cfg)", "startSingleHostReconcile(singleHostApps)"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("main.go never calls %s", want)
		}
	}
}

// On a single host the edge issues every certificate itself: custom domains need no issuer.
func TestCustomDomainsOnASingleHostNeedNoIssuer(t *testing.T) {
	cfg := config.AppConfig{ProvisionerMode: "docker", AppHostingEnabled: true}
	if !customDomainsOn(cfg) {
		t.Fatal("custom domains off on a single host")
	}
	if err := verifyAppDomainIssuer(context.Background(), cfg, nil); err != nil {
		t.Fatalf("a single host asked for a cluster issuer: %v", err)
	}
	cfg.AppHostingEnabled = false
	if customDomainsOn(cfg) {
		t.Fatal("custom domains on without app hosting")
	}
}

func TestWireSingleHostTeardown(t *testing.T) {
	if wireSingleHostTeardown(nil, nil, nil) {
		t.Fatal("wired without a single-host runtime")
	}
}
