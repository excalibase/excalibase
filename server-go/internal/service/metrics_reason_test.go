package service

import (
	"context"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// Samples stored before the reason was cleaned can hold the API server's
// address until the 7-day prune; reading them hides it (EXC-684).
func TestHistoryHidesAnOldRawUnavailableReason(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	svc := NewMetricsService(store, k8s.NewMockClient(), dir)
	history := &fakeMetricsHistory{}
	svc.SetHistory(history, nil)
	raw := `Post "https://10.43.0.1:443/api/v1/namespaces/ns/pods/p-postgres-1/exec": dial tcp 10.43.0.1:443: connect: refused`
	plain := "the metrics exporter answered 503"
	history.rows = []domain.DatabaseMetrics{
		{ProjectID: "p", MetricsAvailable: false, UnavailableReason: &raw},
		{ProjectID: "p", MetricsAvailable: false, UnavailableReason: &plain},
	}
	got, err := svc.GetMetricsHistory(context.Background(), "p", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range got.Metrics {
		reason := *sample.UnavailableReason
		if strings.Contains(reason, "10.43") || strings.Contains(reason, "dial") {
			t.Errorf("reason leaks: %q", reason)
		}
	}
	if *got.Metrics[1].UnavailableReason != plain {
		t.Errorf("a plain reason was changed: %q", *got.Metrics[1].UnavailableReason)
	}
	if history.rows[0].UnavailableReason != &raw || raw[:4] != "Post" {
		t.Error("the stored sample was altered")
	}
}
