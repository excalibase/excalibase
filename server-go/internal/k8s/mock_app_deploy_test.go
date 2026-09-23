package k8s

import (
	"context"
	"errors"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
)

func TestMockClient_ApplyAppWorkload_RecordsByNamespaceAndName(t *testing.T) {
	m := NewMockClient()
	workload := &AppWorkload{Deployment: &appsv1.Deployment{}}
	workload.Deployment.Name = "app-web"

	if err := m.ApplyAppWorkload(context.Background(), "ns1", workload); err != nil {
		t.Fatalf("ApplyAppWorkload: %v", err)
	}
	got, ok := m.AppWorkloads["ns1/app-web"]
	if !ok || got != workload {
		t.Fatalf("AppWorkloads[ns1/app-web]: got %+v", got)
	}
}

func TestMockClient_ApplyAppWorkload_ReturnsScriptedError(t *testing.T) {
	m := NewMockClient()
	m.ApplyAppWorkloadErr = errors.New("apply refused")

	err := m.ApplyAppWorkload(context.Background(), "ns1", &AppWorkload{Deployment: &appsv1.Deployment{}})
	if !errors.Is(err, m.ApplyAppWorkloadErr) {
		t.Fatalf("expected the scripted error, got %v", err)
	}
}

func TestMockClient_WaitForAppRollout_DefaultsToSuccess(t *testing.T) {
	m := NewMockClient()
	if err := m.WaitForAppRollout(context.Background(), "ns1", "app-web", time.Second); err != nil {
		t.Fatalf("expected success with no error scripted, got %v", err)
	}
}

func TestMockClient_WaitForAppRollout_ScriptedErrorByKey(t *testing.T) {
	m := NewMockClient()
	want := errors.New("crash loop")
	m.AppRolloutErr["ns1/app-web"] = want

	if err := m.WaitForAppRollout(context.Background(), "ns1", "app-web", time.Second); !errors.Is(err, want) {
		t.Fatalf("expected the scripted error, got %v", err)
	}
	if err := m.WaitForAppRollout(context.Background(), "ns1", "other-app", time.Second); err != nil {
		t.Fatalf("a different name must not see another app's scripted error, got %v", err)
	}
}

func TestMockClient_WaitForAppRollout_FuncTakesPriorityOverErr(t *testing.T) {
	m := NewMockClient()
	m.AppRolloutErr["ns1/app-web"] = errors.New("would fail via map")
	called := false
	m.AppRolloutFunc = func(ctx context.Context, namespace, name string, timeout time.Duration) error {
		called = true
		return nil
	}

	if err := m.WaitForAppRollout(context.Background(), "ns1", "app-web", time.Second); err != nil {
		t.Fatalf("AppRolloutFunc should have overridden the map error, got %v", err)
	}
	if !called {
		t.Fatal("AppRolloutFunc was not invoked")
	}
}
