package service

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// EXC-516: a backup named for the second it started in shares its name with
// the one before it when two start together, so the second overwrote the first.
func TestK8sAdapterTwoBackupsInOneSecondGetTwoNames(t *testing.T) {
	mock := k8s.NewMockClient()
	adapter := NewK8sBackupAdapter(mock, nil)
	adapter.SetClock(func() time.Time { return time.Date(2026, 11, 20, 10, 15, 30, 500, time.UTC) })
	inst := &domain.DatabaseInstance{ProjectID: "proj-a", Namespace: "ns-a"}

	first, err := adapter.TriggerManual(context.Background(), inst)
	if err != nil {
		t.Fatal(err)
	}
	second, err := adapter.TriggerManual(context.Background(), inst)
	if err != nil {
		t.Fatal(err)
	}

	if first.ID == second.ID {
		t.Fatalf("both backups are named %s", first.ID)
	}
	if len(mock.CRDs) != 2 {
		t.Fatalf("%d backup objects exist, want 2", len(mock.CRDs))
	}
}

func TestMySQLAdapterTwoBackupsInOneSecondGetTwoNames(t *testing.T) {
	lab := newMySQLAdapterLab(t, sourceKeys()...)
	lab.cronJob()
	lab.now = at(11, 58)

	first, err := lab.adapter.TriggerManual(context.Background(), lab.source)
	if err != nil {
		t.Fatal(err)
	}
	second, err := lab.adapter.TriggerManual(context.Background(), lab.source)
	if err != nil {
		t.Fatal(err)
	}

	if first.ID == second.ID {
		t.Fatalf("both backups are named %s", first.ID)
	}
}

func TestBackupStampsNeverRepeatForAProject(t *testing.T) {
	var stamps backupStamps
	now := time.Date(2026, 11, 20, 10, 15, 30, 0, time.UTC)
	seen := map[time.Time]bool{}
	for i := 0; i < 5; i++ {
		stamp := stamps.next("proj-a", now)
		if seen[stamp] {
			t.Fatalf("%v given twice", stamp)
		}
		seen[stamp] = true
	}
	if other := stamps.next("proj-b", now); !other.Equal(now) {
		t.Fatalf("another project's stamp is its own second: %v", other)
	}
	if earlier := stamps.next("proj-a", now.Add(-time.Hour)); !earlier.Equal(now.Add(-time.Hour)) {
		t.Fatalf("a clock that stepped back is taken as it is: %v", earlier)
	}
	if later := stamps.next("proj-a", now.Add(time.Hour)); !later.Equal(now.Add(time.Hour)) {
		t.Fatalf("a later clock is taken as it is: %v", later)
	}
}
