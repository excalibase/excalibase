package main

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
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
	alerts := service.NewAlertingService(t.TempDir())
	if sampler := walArchiveSampler(cfg, kube, alerts, nil); sampler != nil {
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
	if adminAlertNotifier(config.AppConfig{}, nil, nil) != nil {
		t.Error("a notifier was built with nothing to mail through")
	}
}
