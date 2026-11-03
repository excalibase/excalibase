package service

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// limitPostgresPod gives the mock's postgres pod the limits Kubernetes enforces.
func limitPostgresPod(mock *k8s.MockClient, namespace, cpu, memory string) {
	pod := &mock.Pods[namespace][0]
	pod.Spec.Containers = []corev1.Container{{
		Name: "postgres",
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(cpu),
			corev1.ResourceMemory: resource.MustParse(memory),
		}},
	}}
}

func near(got *float64, want float64) bool {
	return got != nil && math.Abs(*got-want) < 0.01
}

// A resized tier: the pod runs at 250m / 256Mi, not the 500m / 512Mi a FREE
// project once had, so usage is measured against what the pod may use.
func TestUsageIsMeasuredAgainstThePodsOwnLimits(t *testing.T) {
	svc, mock := setupMetricsCollectTest(t)
	limitPostgresPod(mock, "org-m-db", "250m", "256Mi")
	mock.Metrics["org-m-db"] = []k8s.PodResourceMetrics{{Name: "m-db-postgres-1", CPUMillis: 200, MemoryMB: 128}}

	m, err := svc.GetCurrentMetrics(context.Background(), "m-db")
	if err != nil {
		t.Fatalf("GetCurrentMetrics: %v", err)
	}
	if !near(m.CPULimitCores, 0.25) || m.MemoryLimitMB == nil || *m.MemoryLimitMB != 256 {
		t.Fatalf("limits = %v cores / %v MB, want 0.25 / 256", deref(m.CPULimitCores), m.MemoryLimitMB)
	}
	if !near(m.CPUUsagePercent, 80) || !near(m.MemoryUsagePercent, 50) {
		t.Fatalf("usage = %v%% cpu / %v%% memory, want 80 / 50", deref(m.CPUUsagePercent), deref(m.MemoryUsagePercent))
	}
	if m.Pods[0].CPULimitCores != 0.25 || m.Pods[0].MemoryLimitMB != 256 {
		t.Fatalf("pod limits = %+v", m.Pods[0])
	}
}

// A pod without limits is measured against its tier's row as an admin set it.
func TestUsageFallsBackToTheTierRow(t *testing.T) {
	svc, mock := setupMetricsCollectTest(t)
	svc.SetTierLimits(func(_ context.Context, tier domain.TierType) (config.TierConfig, error) {
		if tier != domain.Free {
			t.Fatalf("tier = %s", tier)
		}
		return config.TierConfig{Instances: 1, CPU: "0.25", Memory: "1Gi"}, nil
	})
	mock.Metrics["org-m-db"] = []k8s.PodResourceMetrics{{Name: "m-db-postgres-1", CPUMillis: 100, MemoryMB: 512}}

	m, _ := svc.GetCurrentMetrics(context.Background(), "m-db")
	if !near(m.CPULimitCores, 0.25) || m.MemoryLimitMB == nil || *m.MemoryLimitMB != 1024 {
		t.Fatalf("limits = %v cores / %v MB, want 0.25 / 1024", deref(m.CPULimitCores), m.MemoryLimitMB)
	}
	if !near(m.CPUUsagePercent, 40) || !near(m.MemoryUsagePercent, 50) {
		t.Fatalf("usage = %v%% / %v%%, want 40 / 50", deref(m.CPUUsagePercent), deref(m.MemoryUsagePercent))
	}
}

// With no limit to measure against, the usage is reported and the share is not.
func TestUsageShareIsUnsetWithoutALimit(t *testing.T) {
	svc, mock := setupMetricsCollectTest(t)
	svc.SetTierLimits(func(context.Context, domain.TierType) (config.TierConfig, error) {
		return config.TierConfig{}, errors.New("platform DB unreachable")
	})
	mock.Metrics["org-m-db"] = []k8s.PodResourceMetrics{{Name: "m-db-postgres-1", CPUMillis: 100, MemoryMB: 64}}

	m, _ := svc.GetCurrentMetrics(context.Background(), "m-db")
	if m.CPUUsageCores == nil || m.CPUUsagePercent != nil || m.MemoryUsagePercent != nil || m.CPULimitCores != nil {
		t.Fatalf("got cores %v, cpu %v%%, memory %v%%, limit %v; want cores only",
			deref(m.CPUUsageCores), deref(m.CPUUsagePercent), deref(m.MemoryUsagePercent), deref(m.CPULimitCores))
	}
}

func deref(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func TestAnUnreadableTierRowOrPodListNamesNoLimit(t *testing.T) {
	svc, mock := setupMetricsCollectTest(t)
	mock.GetPodsError = errors.New("forbidden")
	svc.SetTierLimits(func(context.Context, domain.TierType) (config.TierConfig, error) {
		return config.TierConfig{Instances: 1, CPU: "half", Memory: "1Gi"}, nil
	})

	m, _ := svc.GetCurrentMetrics(context.Background(), "m-db")
	if m.CPULimitCores != nil || m.CPUUsagePercent != nil {
		t.Fatalf("limit %v, share %v; want neither", deref(m.CPULimitCores), deref(m.CPUUsagePercent))
	}
}

// Only the postgres container's limits count; a sidecar's are not the database's.
func TestASidecarsLimitsAreNotTheDatabases(t *testing.T) {
	svc, mock := setupMetricsCollectTest(t)
	limitPostgresPod(mock, "org-m-db", "250m", "256Mi")
	mock.Pods["org-m-db"][0].Spec.Containers[0].Name = "plugin-barman-cloud"

	m, _ := svc.GetCurrentMetrics(context.Background(), "m-db")
	if m.CPULimitCores != nil {
		t.Fatalf("limit = %v, want none", deref(m.CPULimitCores))
	}
}
