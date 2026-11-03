package main

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/logdelete"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

type fakeLogDeletionStore struct{ queued []logdelete.Deletion }

func (f *fakeLogDeletionStore) EnqueueProjectLogDeletion(_ context.Context, d logdelete.Deletion) error {
	f.queued = append(f.queued, d)
	return nil
}
func (f *fakeLogDeletionStore) ClaimDueProjectLogDeletions(context.Context, time.Time, time.Duration, int) ([]logdelete.Deletion, error) {
	return nil, nil
}
func (f *fakeLogDeletionStore) SaveProjectLogDeletion(context.Context, logdelete.Deletion) error {
	return nil
}
func (f *fakeLogDeletionStore) LogAudit(context.Context, *domain.AuditEntry) error { return nil }

// EXC-678: with the flag off nothing is queued and no worker calls Loki.
func TestLogDeletionStartsOnlyWithItsFeature(t *testing.T) {
	on := config.AppConfig{DarkFeatures: []features.Feature{features.LogDeletion}}
	for name, tc := range map[string]struct {
		cfg   config.AppConfig
		store logDeletionStore
		loki  string
		want  bool
	}{
		"off":         {config.AppConfig{}, &fakeLogDeletionStore{}, "http://loki:3100", false},
		"on, no loki": {on, &fakeLogDeletionStore{}, "", false},
		"on, no db":   {on, nil, "http://loki:3100", false},
		"on":          {on, &fakeLogDeletionStore{}, "http://loki:3100", true},
	} {
		store, _ := storage.NewFileSystemStore(t.TempDir())
		svc := service.NewProvisioningService(store, provisioner.NewFactory(), nil)
		if got := wireLogDeletion(tc.cfg, tc.store, svc, tc.loki) != nil; got != tc.want {
			t.Errorf("%s: worker started = %v, want %v", name, got, tc.want)
		}
	}
}

func TestLongestLogRetention(t *testing.T) {
	retention := config.LogRetention{Default: 168 * time.Hour, ByPlan: map[domain.TierType]time.Duration{domain.Free: 24 * time.Hour, domain.Enterprise: 720 * time.Hour}}
	if got := longestLogRetention(retention); got != 720*time.Hour {
		t.Errorf("longest = %v", got)
	}
	if got := longestLogRetention(config.LogRetention{}); got != 0 {
		t.Errorf("empty = %v", got)
	}
}
