package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
)

func hostingConfig(enabled bool) config.AppConfig {
	return config.AppConfig{AppHostingEnabled: enabled, AppRuntimeClass: "gvisor"}
}

func TestEdgePeerFromConfig(t *testing.T) {
	cfg := config.AppConfig{
		EdgeNamespace: "haproxy-controller",
		EdgePodLabels: map[string]string{"app.kubernetes.io/name": "kubernetes-ingress"},
		EdgePodPorts:  []int{8080, 8443},
	}
	edge := edgePeer(cfg)
	if edge.Namespace != "haproxy-controller" || edge.Labels["app.kubernetes.io/name"] != "kubernetes-ingress" || len(edge.Ports) != 2 {
		t.Fatalf("edge = %+v", edge)
	}
	if empty := edgePeer(config.AppConfig{}); empty.Namespace != "" || len(empty.Ports) != 0 {
		t.Fatalf("no edge configured must give an empty peer, got %+v", empty)
	}
}

func TestVerifyAppRuntime_HostingDisabledChecksNothing(t *testing.T) {
	kube := k8s.NewMockClient()
	if err := verifyAppRuntime(context.Background(), hostingConfig(false), kube); err != nil {
		t.Fatalf("hosting disabled: %v", err)
	}
	if len(kube.Calls) != 0 {
		t.Errorf("hosting disabled must not touch the cluster, calls: %v", kube.Calls)
	}
	if err := verifyAppRuntime(context.Background(), hostingConfig(false), nil); err != nil {
		t.Fatalf("hosting disabled without a cluster: %v", err)
	}
}

func TestVerifyAppRuntime_PresentRuntimeClassPasses(t *testing.T) {
	kube := k8s.NewMockClient()
	kube.RuntimeClasses["gvisor"] = true
	if err := verifyAppRuntime(context.Background(), hostingConfig(true), kube); err != nil {
		t.Fatalf("want success, got %v", err)
	}
}

func TestVerifyAppRuntime_Refusals(t *testing.T) {
	failing := k8s.NewMockClient()
	failing.RuntimeClassError = errors.New("forbidden")
	cases := map[string]struct {
		kube k8s.KubeClient
		want string
	}{
		"missing runtime class": {kube: k8s.NewMockClient(), want: `RuntimeClass "gvisor" (APP_RUNTIME_CLASS) does not exist`},
		"unreadable":            {kube: failing, want: "forbidden"},
		"no cluster":            {kube: nil, want: "no Kubernetes client"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := verifyAppRuntime(context.Background(), hostingConfig(true), tc.kube)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestAppRouteCarriesTheRouteConfig(t *testing.T) {
	cfg := config.AppConfig{
		AppDomain: "apps.example.com", AppIngressClass: "haproxy", AppDomainIssuer: "apps-acme",
		AppIngressFromNamespace: "haproxy-controller", AppIngressFromLabels: map[string]string{"app": "edge"},
	}
	route := appRoute(cfg)
	if route.Domain != "apps.example.com" || route.IngressClass != "haproxy" || route.Issuer != "apps-acme" ||
		route.IngressFromNamespace != "haproxy-controller" || route.IngressFromLabels["app"] != "edge" {
		t.Errorf("appRoute = %+v", route)
	}
	if url, err := route.Public().URL("web", "proj-abc"); err != nil || url != "https://web-abc.apps.example.com" {
		t.Errorf("public URL = %q, %v", url, err)
	}
}

func TestRegistryCredentialWiring(t *testing.T) {
	if registryCredentials(nil, fakestore.NewInstances(), k8s.NewMockClient()) != nil {
		t.Fatal("no vault, no credential store")
	}
	if newRegistryCredentialHandler(nil) == nil {
		t.Fatal("the handler must exist to answer 503")
	}
	creds := registryCredentials(vaultclient.NewHTTPClient("http://vault.invalid", "pat"), fakestore.NewInstances(), k8s.NewMockClient())
	if creds == nil || newRegistryCredentialHandler(creds) == nil {
		t.Fatal("a vault gives a credential store")
	}
	deploys := service.NewAppDeployService(nil, nil, k8s.NewMockClient(), fakestore.NewInstances(), nil, k8s.AppRenderOptions{})
	if withRegistryCredentials(deploys, nil) != deploys || withRegistryCredentials(deploys, creds) != deploys {
		t.Fatal("the deploy service is returned as given")
	}
}
