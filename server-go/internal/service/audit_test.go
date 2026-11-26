package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	preloadQuery  = "shared_preload_libraries"
	createExtStmt = "CREATE EXTENSION IF NOT EXISTS pgaudit"
)

func setupAuditTest(t *testing.T) (*AuditService, *ProvisioningService, *k8s.MockClient) {
	t.Helper()
	prov, store, mock := setupClusterChangeTest(t)
	svc := NewAuditService(store, mock, prov)
	svc.pollEvery = time.Millisecond
	setPrimary(t, mock, testOpsDBPostgres+"-1")
	return svc, prov, mock
}

func auditSetting(t *testing.T, mock *k8s.MockClient) string {
	t.Helper()
	return k8s.AuditLog(opsCluster(t, mock))
}

func setPrimary(t *testing.T, mock *k8s.MockClient, pod string) {
	t.Helper()
	cluster := opsCluster(t, mock)
	if err := unstructured.SetNestedField(cluster.Object, pod, "status", "currentPrimary"); err != nil {
		t.Fatal(err)
	}
	mock.CRDs[testOpsDBNS+"/"+testOpsDBPostgres] = cluster
}

// CNPG preloads pgaudit only once a pgaudit.* setting is on the cluster (EXC-688).
func TestEnableAuditPutsTheSettingOnTheClusterAndKeepsTheRest(t *testing.T) {
	svc, _, mock := setupAuditTest(t)
	mock.ExecAnswer = func([]string) (string, error) { return "pgaudit", nil }
	if err := svc.EnableAudit(context.Background(), testOpsDB, domain.AuditConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	svc.finishing.Wait()
	if got := auditSetting(t, mock); got != defaultAuditClasses {
		t.Errorf("pgaudit.log = %q, want the default %q", got, defaultAuditClasses)
	}
	if clusterField(t, opsCluster(t, mock), "postgresql", "parameters", "work_mem") != "4MB" {
		t.Error("the tenant's settings were lost")
	}
}

// The call returns once the cluster is changed; the restart is waited for in the
// background, so the request fits the edge's timeout.
func TestEnableAuditReturnsBeforeTheLibraryLoadsThenCreatesTheExtension(t *testing.T) {
	svc, _, mock := setupAuditTest(t)
	loaded := make(chan struct{})
	mock.ExecAnswer = func(cmd []string) (string, error) {
		line := strings.Join(cmd, " ")
		if strings.Contains(line, preloadQuery) {
			select {
			case <-loaded:
				return "pg_stat_statements,pgaudit", nil
			default:
				return "pg_stat_statements", nil
			}
		}
		if strings.Contains(line, createExtStmt) {
			select {
			case <-loaded:
			default:
				t.Error("extension created before the library loaded")
			}
		}
		return "", nil
	}
	if err := svc.EnableAudit(context.Background(), testOpsDB, domain.AuditConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	close(loaded)
	svc.finishing.Wait()
	if !strings.Contains(strings.Join(mock.ExecCommands, "\n"), createExtStmt) {
		t.Error("the extension was never created")
	}
}

// A request that gives up does not stop the extension from being created.
func TestEnableAuditFinishesAfterTheRequestIsCancelled(t *testing.T) {
	svc, _, mock := setupAuditTest(t)
	mock.ExecAnswer = func([]string) (string, error) { return "pgaudit", nil }
	ctx, cancel := context.WithCancel(context.Background())
	if err := svc.EnableAudit(ctx, testOpsDB, domain.AuditConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	cancel()
	svc.finishing.Wait()
	if !strings.Contains(strings.Join(mock.ExecCommands, "\n"), createExtStmt) {
		t.Error("the extension was not created after the request was cancelled")
	}
}

// HA tiers switch over on a restart: the primary is not always instance 1.
func TestEnableAuditAndReadsUseTheCurrentPrimary(t *testing.T) {
	svc, _, mock := setupAuditTest(t)
	primary := testOpsDBPostgres + "-2"
	setPrimary(t, mock, primary)
	mock.ExecAnswer = func([]string) (string, error) { return "pgaudit", nil }
	if err := svc.EnableAudit(context.Background(), testOpsDB, domain.AuditConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	svc.finishing.Wait()
	if _, err := svc.GetAuditConfig(context.Background(), testOpsDB); err != nil {
		t.Fatal(err)
	}
	for _, call := range mock.Calls {
		if strings.HasPrefix(call, "ExecInPod:") && !strings.HasSuffix(call, "/"+primary) {
			t.Errorf("%s: want only the current primary %s", call, primary)
		}
	}
	if countCalls(mock, "ExecInPod:") == 0 {
		t.Fatal("nothing ran")
	}
}

func TestEnableAuditFalseIsRefusedAndTouchesNothing(t *testing.T) {
	svc, _, mock := setupAuditTest(t)
	err := svc.EnableAudit(context.Background(), testOpsDB, domain.AuditConfig{Enabled: false})
	if !errors.Is(err, ErrAuditDisableUnsupported) {
		t.Fatalf("err = %v", err)
	}
	if countCalls(mock, "UpdateCRD:") != 0 || auditSetting(t, mock) != "" {
		t.Error("the cluster was changed")
	}
}

func TestEnableAuditRefusesAProjectThatIsNotActive(t *testing.T) {
	svc, prov, mock := setupAuditTest(t)
	inst, err := prov.store.FindByProjectID(testOpsDB)
	if err != nil {
		t.Fatal(err)
	}
	inst.Status = "PAUSED"
	if err := prov.store.Update(inst); err != nil {
		t.Fatal(err)
	}
	err = svc.EnableAudit(context.Background(), testOpsDB, domain.AuditConfig{Enabled: true})
	if !errors.Is(err, ErrProjectNotActive) {
		t.Fatalf("err = %v, want ErrProjectNotActive", err)
	}
	if auditSetting(t, mock) != "" {
		t.Error("the cluster was changed")
	}
}

// A second call while the setting is there writes nothing, so it cannot restart again.
func TestEnableAuditTwiceDoesNotWriteTheClusterAgain(t *testing.T) {
	svc, _, mock := setupAuditTest(t)
	mock.ExecAnswer = func([]string) (string, error) { return "pgaudit", nil }
	for range 2 {
		if err := svc.EnableAudit(context.Background(), testOpsDB, domain.AuditConfig{Enabled: true}); err != nil {
			t.Fatal(err)
		}
		svc.finishing.Wait()
	}
	if got := countCalls(mock, "UpdateCRD:"); got != 1 {
		t.Errorf("cluster written %d times, want 1", got)
	}
}

func TestAuditLogValue(t *testing.T) {
	cases := []struct {
		name, value string
		named       bool
		want        string
		bad         bool
	}{
		{name: "default", want: "ddl,role,write"},
		{name: "joined and trimmed", named: true, value: " ddl , write ", want: "ddl,write"},
		{name: "one class", named: true, value: "all", want: "all"},
		{name: "unknown class", named: true, value: "write,drop", bad: true},
		{name: "injection", named: true, value: "write; DROP TABLE x", bad: true},
		{name: "newline", named: true, value: "ddl,\nwrite", bad: true},
		{name: "space inside a class", named: true, value: "wri te", bad: true},
		{name: "empty", named: true, value: "", bad: true},
		{name: "empty class", named: true, value: "ddl,,write", bad: true},
	}
	for _, c := range cases {
		cfg := domain.AuditConfig{Enabled: true}
		if c.named {
			cfg.Settings = map[string]string{"pgaudit.log": c.value}
		}
		got, err := AuditLogValue(cfg)
		if c.bad {
			if !errors.Is(err, ErrAuditClasses) {
				t.Errorf("%s: err = %v, want ErrAuditClasses", c.name, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: got %q, %v want %q", c.name, got, err, c.want)
		}
	}
}

func TestEnableAuditIsRefusedOnASingleHost(t *testing.T) {
	svc, prov, _ := setupAuditTest(t)
	inst, err := prov.store.FindByProjectID(testOpsDB)
	if err != nil {
		t.Fatal(err)
	}
	inst.DeploymentMode = domain.ModeDocker
	if err := prov.store.Update(inst); err != nil {
		t.Fatal(err)
	}
	err = svc.EnableAudit(context.Background(), testOpsDB, domain.AuditConfig{Enabled: true})
	if !errors.Is(err, ErrKubernetesOnly) {
		t.Fatalf("err = %v, want ErrKubernetesOnly", err)
	}
}

// answerAuditConfig answers the settings query with settings and the extension
// query with extensions rows.
func answerAuditConfig(mock *k8s.MockClient, settings, extensions string) {
	mock.ExecAnswer = func(cmd []string) (string, error) {
		if strings.Contains(strings.Join(cmd, " "), "pg_extension") {
			return extensions, nil
		}
		return settings, nil
	}
}

func TestGetAuditConfigIsEnabledOnlyWithTheLibraryAndTheExtension(t *testing.T) {
	const settings = "pgaudit.log|ddl\npgaudit.log_level|log"
	cases := []struct {
		name, settings, extensions, status string
		enabled                            bool
	}{
		{"both", settings, "1", AuditStatusEnabled, true},
		{"preloaded, no extension yet", settings, "0", AuditStatusPending, false},
		{"preloaded, extension unreadable", settings, "", AuditStatusPending, false},
		{"nothing", "", "0", AuditStatusDisabled, false},
	}
	for _, c := range cases {
		svc, _, mock := setupAuditTest(t)
		answerAuditConfig(mock, c.settings, c.extensions)
		cfg, err := svc.GetAuditConfig(context.Background(), testOpsDB)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Status != c.status || cfg.Enabled != c.enabled {
			t.Errorf("%s: status %q enabled %v, want %q %v", c.name, cfg.Status, cfg.Enabled, c.status, c.enabled)
		}
	}
}

// A copy of a database whose extension exists needs the library preloaded, or
// every DDL statement on it fails (EXC-688). The record is what the restore reads.
func TestK8sRestoreKeepsTheSourcesAuditSetting(t *testing.T) {
	for _, source := range []string{"", "ddl,write"} {
		mock := k8s.NewMockClient()
		src := sourceInstance()
		if source != "" {
			src.Parameters = map[string]string{k8s.AuditLogParameter: source}
		}
		adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})
		if _, err := adapter.Restore(context.Background(), src, domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"}); err != nil {
			t.Fatalf("Restore: %v", err)
		}
		if got := k8s.AuditLog(mock.CRDs["org-dst/dst-postgres"]); got != source {
			t.Errorf("source audit %q: restored cluster has %q", source, got)
		}
	}
}

func TestEnableAuditRecordsTheSettingAndTuningKeepsIt(t *testing.T) {
	svc, prov, mock := setupAuditTest(t)
	mock.ExecAnswer = func([]string) (string, error) { return "pgaudit", nil }
	if err := svc.EnableAudit(context.Background(), testOpsDB, domain.AuditConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	svc.finishing.Wait()
	inst, err := prov.store.FindByProjectID(testOpsDB)
	if err != nil || inst.Parameters[k8s.AuditLogParameter] != defaultAuditClasses {
		t.Fatalf("recorded parameters %v, err %v", inst.Parameters, err)
	}
	if err := prov.TuneParameters(context.Background(), testOpsDB, map[string]string{"work_mem": "8MB"}); err != nil {
		t.Fatal(err)
	}
	inst, _ = prov.store.FindByProjectID(testOpsDB)
	if inst.Parameters[k8s.AuditLogParameter] != defaultAuditClasses || inst.Parameters["work_mem"] != "8MB" {
		t.Errorf("after tuning the record is %v", inst.Parameters)
	}
}
