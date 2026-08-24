//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestBackupSchedules_RoundTrip(t *testing.T) {
	store := testStore(t)
	ss := NewBackupSchedules(store)
	ctx := context.Background()

	if err := ss.UpsertSchedule(ctx, &domain.BackupSchedule{
		ProjectID: "p1", Cron: "0 0 * * *", RetentionDays: 7, Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := ss.ListEnabledSchedules(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Cron != "0 0 * * *" {
		t.Errorf("schedule: %+v", got)
	}
}

func TestBackupSchedules_DisabledNotListed(t *testing.T) {
	store := testStore(t)
	ss := NewBackupSchedules(store)
	ctx := context.Background()

	ss.UpsertSchedule(ctx, &domain.BackupSchedule{ProjectID: "p1", Cron: "0 0 * * *", Enabled: false})
	got, _ := ss.ListEnabledSchedules(ctx)
	if len(got) != 0 {
		t.Errorf("disabled schedule listed: %+v", got)
	}
}

func TestBackupSchedules_UpsertOverwrites(t *testing.T) {
	store := testStore(t)
	ss := NewBackupSchedules(store)
	ctx := context.Background()

	ss.UpsertSchedule(ctx, &domain.BackupSchedule{ProjectID: "p1", Cron: "0 0 * * *", RetentionDays: 7, Enabled: true})
	ss.UpsertSchedule(ctx, &domain.BackupSchedule{ProjectID: "p1", Cron: "0 12 * * *", RetentionDays: 30, Enabled: true})

	got, _ := ss.ListEnabledSchedules(ctx)
	if len(got) != 1 || got[0].Cron != "0 12 * * *" || got[0].RetentionDays != 30 {
		t.Errorf("after upsert: %+v", got)
	}
}
