package main

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/email"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

func failingArchiveCluster(since time.Time) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster",
		"metadata": map[string]interface{}{"name": "p1-postgres", "namespace": "ns-p1"},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"type": "ContinuousArchiving", "status": "False", "lastTransitionTime": since.UTC().Format(time.RFC3339)},
		}},
	}}
}

func archiveWiringRun(t *testing.T, cfg config.AppConfig) []string {
	t.Helper()
	kube := k8s.NewMockClient()
	kube.CRDs["ns-p1/p1-postgres"] = failingArchiveCluster(time.Now().Add(-2 * time.Hour))
	instances := fakestore.NewInstances()
	_ = instances.Create(&domain.DatabaseInstance{ProjectID: "p1", OrgID: "org", Namespace: "ns-p1", DBType: domain.PostgreSQL, DeploymentMode: domain.ModeK8s, Status: "ACTIVE"})
	alerts := service.NewAlertingService(t.TempDir())
	if sampler := walArchiveSampler(cfg, kube, instances, alerts, nil); sampler != nil {
		sampler.Sample(context.Background())
	}
	var metrics []string
	for _, alert := range alerts.GetActiveAlerts() {
		metrics = append(metrics, alert.Metric)
	}
	return metrics
}

// EXC-784: on by default, so a database that cannot archive is reported with nothing set.
func TestWALArchiveSampler_RaisesWhenTheGuardIsOn(t *testing.T) {
	cfg := config.AppConfig{DarkFeatures: []features.Feature{features.WALArchiveAlert}, WALArchiveAlertAfter: 30 * time.Minute}
	if got := archiveWiringRun(t, cfg); len(got) != 1 || got[0] != "wal.archiving:ns-p1/p1-postgres" {
		t.Fatalf("alerts = %v", got)
	}
}

func TestWALArchiveSampler_OffWhenTheGuardIsOff(t *testing.T) {
	if got := archiveWiringRun(t, config.AppConfig{WALArchiveAlertAfter: 30 * time.Minute}); len(got) != 0 {
		t.Fatalf("alerts = %v", got)
	}
}

// Single-host databases have no CloudNativePG conditions to read.
func TestWALArchiveSampler_NoneOnSingleHost(t *testing.T) {
	cfg := config.AppConfig{ProvisionerMode: "docker", DarkFeatures: []features.Feature{features.WALArchiveAlert}, WALArchiveAlertAfter: time.Minute}
	if got := archiveWiringRun(t, cfg); len(got) != 0 {
		t.Fatalf("alerts = %v", got)
	}
}

func TestSamplerGroup_RunsEverySampler(t *testing.T) {
	var ran int
	group := service.SamplerGroup{countingSampler{&ran}, countingSampler{&ran}}
	group.Sample(context.Background())
	if ran != 2 {
		t.Fatalf("ran %d, want 2", ran)
	}
}

type countingSampler struct{ n *int }

func (c countingSampler) Sample(context.Context) { *c.n++ }

// The renewer alerts only while the guard is on, and the admin mail is only built when a mailer is set.
func TestRenewalAlertsFollowTheGuard(t *testing.T) {
	alerts := service.NewAlertingService(t.TempDir())
	renewer := service.NewBackupCredentialRenewer(service.BackupCredentialRenewerConfig{})
	on := config.AppConfig{DarkFeatures: []features.Feature{features.WALArchiveAlert}, WALArchiveAlertAfter: time.Minute}
	if !wireRenewalAlerts(on, renewer, alerts, nil) {
		t.Error("guard on: renewer alerts not wired")
	}
	if wireRenewalAlerts(config.AppConfig{}, renewer, alerts, nil) {
		t.Error("guard off: renewer alerts wired")
	}
	if wireRenewalAlerts(on, renewer, nil, nil) {
		t.Error("no alert service: renewer alerts wired")
	}
}

func TestAdminAlertNotifier_NilWithoutAMailerOrUserTable(t *testing.T) {
	if adminAlertNotifier(config.AppConfig{}, nil, nil, nil) != nil {
		t.Error("a notifier was built with nothing to mail through")
	}
}

// The single-host sweep alerts under the same guard and threshold as the cluster check.
func TestHostWALAlertsFollowTheGuard(t *testing.T) {
	alerts := service.NewAlertingService(t.TempDir())
	on := config.AppConfig{DarkFeatures: []features.Feature{features.WALArchiveAlert}, WALArchiveAlertAfter: 45 * time.Minute}
	got := hostWALAlertsOf(on, nil, alerts, nil)
	if !got.on || got.after != 45*time.Minute || got.alerts != alerts {
		t.Errorf("guard on: %+v", got)
	}
	if hostWALAlertsOf(config.AppConfig{}, nil, alerts, nil).on {
		t.Error("guard off: alerts wired")
	}
	if hostWALAlertsOf(on, nil, nil, nil).on {
		t.Error("no alert service: alerts wired")
	}
}

type stubMailer struct{}

func (stubMailer) Send(context.Context, email.Message) (string, error) { return "id", nil }

func TestAdminAlertNotifier_BuiltWithoutAnAlertTableToo(t *testing.T) {
	if adminAlertNotifier(config.AppConfig{}, &tablePlatformStore{}, stubMailer{}, nil) == nil {
		t.Error("no notifier when only the alert table is missing")
	}
}

func TestAdminAlertNotifier_BuiltWhenAMailerAndUserTableExist(t *testing.T) {
	if adminAlertNotifier(config.AppConfig{}, &tablePlatformStore{}, stubMailer{}, service.NewAlertingService(t.TempDir())) == nil {
		t.Error("no notifier with a mailer and a user table")
	}
	if adminAlertNotifier(config.AppConfig{}, &tablePlatformStore{}, nil, nil) != nil {
		t.Error("a notifier was built without a mailer")
	}
}

func TestNewHostWALSweep_BuildsWithAndWithoutAlerts(t *testing.T) {
	alerts := service.NewAlertingService(t.TempDir())
	for _, wired := range []hostWALAlerts{{}, {on: true, alerts: alerts, after: time.Minute}} {
		if newHostWALSweep(nil, nil, wired) == nil {
			t.Errorf("no sweep for %+v", wired)
		}
	}
}

// With no scheduler there is nothing to hand out, and nothing may be touched.
func TestWireBackupScheduler_NilSchedulerTouchesNothing(t *testing.T) {
	wireBackupScheduler(nil, nil, nil)
}
