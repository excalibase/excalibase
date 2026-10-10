package service

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// The Kubernetes client stays the app runtime the services drive (EXC-575):
// a method it lacks fails the build, not a deploy.
var (
	_ AppRuntime = k8s.KubeClient(nil)
	_ AppRuntime = (*k8s.Client)(nil)
)

func TestAppServicesTakeTheAppRuntime(t *testing.T) {
	var runtime AppRuntime = k8s.NewMockClient()
	if NewAppLogService(nil, nil, runtime) == nil {
		t.Fatal("log service")
	}
	if NewAppNetworkService(nil, nil, runtime, nil) == nil {
		t.Fatal("network service")
	}
	if NewRegistryCredentialService(nil, nil, runtime) == nil {
		t.Fatal("registry credential service")
	}
	if NewAppDeployService(nil, nil, runtime, nil, nil, k8s.AppRenderOptions{}) == nil {
		t.Fatal("deploy service")
	}
}
