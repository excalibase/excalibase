package service

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

func storedTier(t *testing.T, tier domain.TierType) config.TierConfig {
	t.Helper()
	tc, err := config.GetTierConfig(tier)
	if err != nil {
		t.Fatal(err)
	}
	tc.ArchiveTimeout = "" // the tier_configs table has no such column
	return tc
}

func tierWithStore(t *testing.T, flags features.Flags) *ProvisioningService {
	t.Helper()
	svc := newRotationHarness(t).svc
	svc.SetFeatures(flags)
	svc.SetTierStore(fakeTierStore{m: map[domain.TierType]config.TierConfig{
		domain.Free: storedTier(t, domain.Free), domain.Standard: storedTier(t, domain.Standard),
	}})
	return svc
}

// EXC-784: the tier table has no archive column, so Free's hourly upload comes from the built-in plan.
func TestTierConfig_FreeShipsWALHourlyWhenTheGuardIsOn(t *testing.T) {
	svc := tierWithStore(t, features.NewStatic(features.FreeWALUpload))
	free, err := svc.tierConfig(context.Background(), domain.Free)
	if err != nil || free.ArchiveTimeout != "1h" {
		t.Fatalf("Free archive timeout = %q, err %v; want 1h", free.ArchiveTimeout, err)
	}
	standard, err := svc.tierConfig(context.Background(), domain.Standard)
	if err != nil || standard.ArchiveTimeout != "" {
		t.Fatalf("Standard archive timeout = %q, err %v; want none", standard.ArchiveTimeout, err)
	}
}

func TestTierConfig_GuardOffLeavesTheOperatorsInterval(t *testing.T) {
	for name, svc := range map[string]*ProvisioningService{
		"stored tier": tierWithStore(t, features.NewStatic()),
		"built-in":    newRotationHarness(t).svc,
	} {
		free, err := svc.tierConfig(context.Background(), domain.Free)
		if err != nil || free.ArchiveTimeout != "" {
			t.Errorf("%s: Free archive timeout = %q, err %v; want none", name, free.ArchiveTimeout, err)
		}
	}
}

func (h *rotationHarness) seedClusterWithArchiveTimeout(timeout string) {
	cluster := k8s.BuildPostgreSQLCluster(k8s.PostgreSQLClusterOpts{ProjectID: rotProject, Namespace: rotNamespace,
		Tier: config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5", ArchiveTimeout: timeout}})
	h.kube.CRDs[rotNamespace+"/"+rotProject+"-postgres"] = cluster
}

func (h *rotationHarness) clusterArchiveTimeout() (string, bool) {
	cluster := h.kube.CRDs[rotNamespace+"/"+rotProject+"-postgres"]
	value, found, _ := unstructured.NestedString(cluster.Object, "spec", "postgresql", "parameters", "archive_timeout")
	return value, found
}

func archiveReconciler(t *testing.T, flags features.Flags) *rotationHarness {
	t.Helper()
	h := newRotationHarness(t)
	h.svc.SetFeatures(flags)
	return h
}

// Existing Free databases get the setting on the next reconcile pass, with no restart.
func TestReconcileArchiveTimeouts_MovesAnExistingFreeClusterToHourly(t *testing.T) {
	h := archiveReconciler(t, features.NewStatic(features.FreeWALUpload))
	h.seedClusterWithArchiveTimeout("")

	report := h.svc.ReconcileArchiveTimeouts(context.Background())
	if len(report.Failed) != 0 || report.Updated != 1 {
		t.Fatalf("report = %+v", report)
	}
	if got, _ := h.clusterArchiveTimeout(); got != "1h" {
		t.Fatalf("archive_timeout = %q, want 1h", got)
	}
}

func TestReconcileArchiveTimeouts_LeavesACorrectClusterAlone(t *testing.T) {
	h := archiveReconciler(t, features.NewStatic(features.FreeWALUpload))
	h.seedClusterWithArchiveTimeout("1h")
	h.kube.Calls = nil

	if report := h.svc.ReconcileArchiveTimeouts(context.Background()); report.Updated != 0 || len(report.Failed) != 0 {
		t.Fatalf("report = %+v", report)
	}
	for _, call := range h.kube.Calls {
		if call == "UpdateCRD:"+rotNamespace+"/clusters" {
			t.Fatal("a cluster already at 1h was written again")
		}
	}
}

func TestReconcileArchiveTimeouts_GuardOffChangesNothing(t *testing.T) {
	h := archiveReconciler(t, features.NewStatic())
	h.seedClusterWithArchiveTimeout("")
	if report := h.svc.ReconcileArchiveTimeouts(context.Background()); report.Updated != 0 {
		t.Fatalf("report = %+v", report)
	}
	if _, found := h.clusterArchiveTimeout(); found {
		t.Fatal("archive_timeout was set with the guard off")
	}
}

func TestReconcileArchiveTimeouts_NonFreeProjectKeepsItsInterval(t *testing.T) {
	h := archiveReconciler(t, features.NewStatic(features.FreeWALUpload))
	inst, _ := h.store.FindByProjectID(rotProject)
	inst.Tier = domain.Standard
	if err := h.store.Update(inst); err != nil {
		t.Fatal(err)
	}
	h.seedClusterWithArchiveTimeout("")
	if report := h.svc.ReconcileArchiveTimeouts(context.Background()); report.Updated != 0 {
		t.Fatalf("report = %+v", report)
	}
}

// A paused project's cluster is hibernated; the setting still lands so the resume ships hourly.
func TestReconcileArchiveTimeouts_ReachesAPausedProject(t *testing.T) {
	h := archiveReconciler(t, features.NewStatic(features.FreeWALUpload))
	h.seedClusterWithArchiveTimeout("")
	inst, _ := h.store.FindByProjectID(rotProject)
	inst.Status = string(domain.StatusPaused)
	_ = h.store.Update(inst)
	if report := h.svc.ReconcileArchiveTimeouts(context.Background()); report.Updated != 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestReconcileArchiveTimeouts_ReportsAClusterItCannotWrite(t *testing.T) {
	h := archiveReconciler(t, features.NewStatic(features.FreeWALUpload))
	h.seedClusterWithArchiveTimeout("")
	h.kube.UpdateCRDError = errors.New("conflict forever")
	report := h.svc.ReconcileArchiveTimeouts(context.Background())
	if len(report.Failed) != 1 || report.Updated != 0 {
		t.Fatalf("report = %+v", report)
	}
}

func TestReconcileArchiveTimeouts_NothingToDoOnSingleHost(t *testing.T) {
	h := archiveReconciler(t, features.NewStatic(features.FreeWALUpload))
	h.svc.k8sClient = nil
	if report := h.svc.ReconcileArchiveTimeouts(context.Background()); report.Updated != 0 || len(report.Failed) != 0 {
		t.Fatalf("report = %+v", report)
	}
}

func TestLogArchiveTimeouts_LogsFailuresAndUpdates(t *testing.T) {
	var out bytes.Buffer
	log.SetOutput(&out)
	defer log.SetOutput(os.Stderr)
	logArchiveTimeouts(ArchiveTimeoutReport{Updated: 2, Failed: []error{errors.New("boom")}})
	for _, want := range []string{"ERROR: archive timeout reconcile: boom", "2 cluster(s)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("log %q lacks %q", out.String(), want)
		}
	}
}
