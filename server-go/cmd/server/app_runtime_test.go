package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

func hostingConfig(enabled bool) config.AppConfig {
	return config.AppConfig{AppHostingEnabled: enabled, AppRuntimeClass: "gvisor"}
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
