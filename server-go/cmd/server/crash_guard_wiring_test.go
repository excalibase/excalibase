package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/email"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

// EXC-839: the guard and the digest pin are wired at boot, and only the flags start them.
func TestCrashGuard_StartedAtBootAndPinFlagsHandedToDeploys(t *testing.T) {
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"startCrashGuard(cfg, sqlStore, store, k8sClient, deps)", "appDeploySvc.SetFeatureFlags(cfg.Features())"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("main.go never calls %s", want)
		}
	}
}

func TestCrashGuard_WantedOnlyWithHostingAndTheFlag(t *testing.T) {
	hosting := config.AppConfig{AppHostingEnabled: true}
	cases := []struct {
		cfg   config.AppConfig
		flags features.Flags
		want  bool
	}{
		{hosting, features.NewStatic(), false},
		{hosting, nil, false},
		{hosting, features.NewStatic(features.Pipeline), false},
		{hosting, features.NewStatic(features.CrashLoopGuard), true},
		{config.AppConfig{}, features.NewStatic(features.CrashLoopGuard), false},
	}
	for _, tc := range cases {
		if got := crashGuardWanted(t.Context(), tc.cfg, tc.flags); got != tc.want {
			t.Errorf("hosting=%v flags=%v: wanted=%v, want %v", tc.cfg.AppHostingEnabled, tc.flags, got, tc.want)
		}
	}
}

func TestCrashGuard_NoGuardWithoutAKubernetesClient(t *testing.T) {
	cfg := config.AppConfig{AppHostingEnabled: true}
	deploys := service.NewAppDeployService(nil, nil, k8s.NewMockClient(), fakestore.NewInstances(), nil, k8s.AppRenderOptions{})
	// A mock client reads no pods, so nothing is started and a stop function still comes back.
	stop := newCrashGuardStarter(cfg, crashGuardDeps{flags: features.NewStatic(features.CrashLoopGuard), kube: k8s.NewMockClient(), deploys: deploys})
	if stop == nil {
		t.Fatal("a stop function is always returned")
	}
	stop()
}

func TestCrashGuard_ProtectsThePlatformsOwnNamespaces(t *testing.T) {
	cfg := config.AppConfig{PodNamespace: "excalibase-platform", EdgeNamespace: "edge"}
	got := crashGuardProtectedNamespaces(cfg)
	for _, want := range []string{"excalibase-platform", "edge", "kube-system", "default"} {
		found := false
		for _, namespace := range got {
			found = found || namespace == want
		}
		if !found {
			t.Errorf("%s is not protected: %v", want, got)
		}
	}
}

type noMembers struct{}

func (noMembers) ListOrgMembers(context.Context, string) ([]*domain.OrgMember, error) {
	return nil, nil
}

type liveSender struct{}

func (liveSender) Send(context.Context, email.Message) (string, error) { return "id", nil }

func TestCrashStopNotifier_NeedsAConfiguredSenderAndMembers(t *testing.T) {
	cfg := config.AppConfig{}
	if crashStopNotifier(cfg, email.NewNoopSender(), noMembers{}) != nil {
		t.Error("a no-op sender must leave the owners unmailed")
	}
	if crashStopNotifier(cfg, liveSender{}, nil) != nil {
		t.Error("no member source, no mail")
	}
	if crashStopNotifier(cfg, liveSender{}, noMembers{}) == nil {
		t.Error("a configured sender must mail")
	}
}
