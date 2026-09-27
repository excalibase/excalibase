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
	if err := m.WaitForAppRollout(context.Background(), "ns1", "app-web", "dep-1", time.Second); err != nil {
		t.Fatalf("expected success with no error scripted, got %v", err)
	}
}

func TestMockClient_WaitForAppRollout_ScriptedErrorByKey(t *testing.T) {
	m := NewMockClient()
	want := errors.New("crash loop")
	m.AppRolloutErr["ns1/app-web"] = want

	if err := m.WaitForAppRollout(context.Background(), "ns1", "app-web", "dep-1", time.Second); !errors.Is(err, want) {
		t.Fatalf("expected the scripted error, got %v", err)
	}
	if err := m.WaitForAppRollout(context.Background(), "ns1", "other-app", "dep-1", time.Second); err != nil {
		t.Fatalf("a different name must not see another app's scripted error, got %v", err)
	}
}

func TestMockClient_WaitForAppRollout_FuncTakesPriorityOverErr(t *testing.T) {
	m := NewMockClient()
	m.AppRolloutErr["ns1/app-web"] = errors.New("would fail via map")
	called := false
	m.AppRolloutFunc = func(ctx context.Context, namespace, name, deployID string, timeout time.Duration) error {
		called = true
		return nil
	}

	if err := m.WaitForAppRollout(context.Background(), "ns1", "app-web", "dep-1", time.Second); err != nil {
		t.Fatalf("AppRolloutFunc should have overridden the map error, got %v", err)
	}
	if !called {
		t.Fatal("AppRolloutFunc was not invoked")
	}
}

func TestMockClient_AppLifecycle(t *testing.T) {
	m := NewMockClient()
	ctx := context.Background()
	if err := m.PauseAppWorkload(ctx, "ns1", "a1"); err != nil || !m.AppPaused["ns1/a1"] {
		t.Fatalf("pause: %v %v", err, m.AppPaused)
	}
	if err := m.ResumeAppWorkload(ctx, "ns1", "a1", "web", time.Second); err != nil || m.AppPaused["ns1/a1"] {
		t.Fatalf("resume: %v %v", err, m.AppPaused)
	}
	if err := m.WaitForAppPodsGone(ctx, "ns1", "a1", time.Second); err != nil {
		t.Fatalf("pods gone: %v", err)
	}
	if err := m.DeleteAppWorkload(ctx, "ns1", "a1", time.Second); err != nil || !m.AppDeleted["ns1/a1"] {
		t.Fatalf("delete: %v %v", err, m.AppDeleted)
	}

	if err := m.PruneAppWorkload(ctx, "ns1", "a1", "web", time.Second); err != nil {
		t.Fatalf("prune: %v", err)
	}
	boom := errors.New("boom")
	m.AppPruneErr = boom
	if err := m.PruneAppWorkload(ctx, "ns1", "a1", "web", time.Second); !errors.Is(err, boom) {
		t.Fatalf("prune: %v", err)
	}
	m.AppPauseErr, m.AppResumeErr, m.AppPodsGoneErr, m.AppDeleteErr = boom, boom, boom, boom
	for name, err := range map[string]error{
		"pause":     m.PauseAppWorkload(ctx, "ns1", "a1"),
		"resume":    m.ResumeAppWorkload(ctx, "ns1", "a1", "web", time.Second),
		"pods gone": m.WaitForAppPodsGone(ctx, "ns1", "a1", time.Second),
		"delete":    m.DeleteAppWorkload(ctx, "ns1", "a2", time.Second),
	} {
		if !errors.Is(err, boom) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestMockClient_DeleteRegistryPullSecrets(t *testing.T) {
	m := NewMockClient()
	if err := m.DeleteRegistryPullSecrets(context.Background(), "ns1", "ghcr.io"); err != nil || len(m.PullSecretsDeleted) != 1 {
		t.Fatalf("delete: %v %v", err, m.PullSecretsDeleted)
	}
	m.PullSecretsDeleteErr = errors.New("boom")
	if err := m.DeleteRegistryPullSecrets(context.Background(), "ns1", "ghcr.io"); err == nil {
		t.Fatal("want the scripted error")
	}
}

func TestMockClient_AppLogs(t *testing.T) {
	m := NewMockClient()
	m.AppLogLines = map[string][]AppLogLine{"ns1/a1": {{Pod: "p", Text: "hi"}}}
	page, err := m.AppLogs(context.Background(), "ns1", "a1", AppLogOptions{TailLines: 5})
	if err != nil || len(page.Lines) != 1 || m.AppLogsAsked[0].TailLines != 5 {
		t.Fatalf("AppLogs = %v, %v", page, err)
	}
	m.AppLogsErr = errors.New("boom")
	if _, err := m.AppLogs(context.Background(), "ns1", "a1", AppLogOptions{}); err == nil {
		t.Fatal("want the scripted error")
	}
}

func TestMockClient_AppCapacityAnswers(t *testing.T) {
	m := NewMockClient()
	m.LivePods = map[string]AppPods{"ns1/a1": {Count: 2}}
	m.Placement = RuntimePlacement{OverheadCPUMilli: 10, OverheadMemBytes: 20}
	if pods, err := m.LiveAppPods(context.Background(), "ns1", "a1"); pods.Count != 2 || err != nil {
		t.Fatalf("LiveAppPods = %+v %v", pods, err)
	}
	if placement, err := m.RuntimeClassPlacement(context.Background(), "gvisor"); placement.OverheadCPUMilli != 10 || err != nil {
		t.Fatalf("placement = %+v %v", placement, err)
	}
}

func TestMockClient_PausedAppSize(t *testing.T) {
	m := NewMockClient()
	m.PausedSize = PausedApp{Replicas: 2, CPURequest: "50m", MemoryRequest: "128Mi"}
	if got, err := m.PausedAppSize(context.Background(), "ns1", "app-1", "web"); err != nil || got != m.PausedSize {
		t.Fatalf("PausedAppSize = %+v, %v", got, err)
	}
	m.PausedSizeErr = ErrAppNotPaused
	if _, err := m.PausedAppSize(context.Background(), "ns1", "app-1", "web"); !errors.Is(err, ErrAppNotPaused) {
		t.Fatalf("err = %v", err)
	}
}
