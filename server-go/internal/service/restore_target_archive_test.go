package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// sourceDatabase answers the guard's psql calls on the source primary the way
// Postgres would: the clock check, the WAL switch, then pg_stat_archiver.
type sourceDatabase struct {
	*k8s.MockClient
	targetPassed string
	segment      string
	archived     []string
	execErr      error
	commands     []string
}

func (s *sourceDatabase) ExecInPod(_ context.Context, namespace, pod, _ string, cmd []string) (string, error) {
	joined := strings.Join(cmd, " ")
	s.commands = append(s.commands, namespace+"/"+pod+": "+joined)
	if s.execErr != nil {
		return "", s.execErr
	}
	switch {
	case strings.Contains(joined, "clock_timestamp()"):
		return s.targetPassed + "\n", nil
	case strings.Contains(joined, "pg_switch_wal()"):
		return "4711\n" + s.segment + "\n", nil
	case strings.Contains(joined, "pg_stat_archiver"):
		if len(s.archived) == 0 {
			return "\n", nil
		}
		next := s.archived[0]
		if len(s.archived) > 1 {
			s.archived = s.archived[1:]
		}
		return next + "\n", nil
	}
	return "", errors.New("unexpected command: " + joined)
}

func runningSource(mock *k8s.MockClient, primary string, annotations map[string]string) {
	cluster := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster",
		"metadata": map[string]interface{}{"name": "src-postgres", "namespace": "org-src"},
		"status":   map[string]interface{}{"currentPrimary": primary},
	}}
	cluster.SetAnnotations(annotations)
	mock.CRDs["org-src/src-postgres"] = cluster
}

func steppingPoller(timeout time.Duration) provisioner.Poller {
	now := time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC)
	return provisioner.Poller{
		Interval: time.Second, Timeout: timeout,
		Now: func() time.Time { return now },
		After: func(d time.Duration) <-chan time.Time {
			now = now.Add(d)
			ch := make(chan time.Time, 1)
			ch <- now
			return ch
		},
	}
}

func newTestArchiveGuard(db *sourceDatabase, timeout time.Duration) *ArchivedWALGuard {
	guard := NewArchivedWALGuard(db)
	guard.poller = steppingPoller(timeout)
	guard.now = func() time.Time { return time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC) }
	return guard
}

var drillTarget = time.Date(2026, 9, 28, 2, 18, 6, 782_000_000, time.UTC)

func TestArchiveGuardSwitchesWALAndWaitsUntilTheTargetIsArchived(t *testing.T) {
	db := &sourceDatabase{MockClient: k8s.NewMockClient(), targetPassed: "t", segment: "000000010000000000000006",
		archived: []string{"000000010000000000000005", "000000010000000000000005", "000000010000000000000006"}}
	runningSource(db.MockClient, "src-postgres-2", nil)
	db.PodReady["org-src/src-postgres-2"] = true

	if err := newTestArchiveGuard(db, time.Minute).EnsureRecoverable(context.Background(), sourceInstance(), drillTarget); err != nil {
		t.Fatalf("EnsureRecoverable: %v", err)
	}
	if len(db.commands) != 5 {
		t.Fatalf("want clock check, switch, three archiver reads; got %v", db.commands)
	}
	for _, c := range db.commands {
		if !strings.HasPrefix(c, "org-src/src-postgres-2: ") {
			t.Errorf("every statement must run on the current primary, got %q", c)
		}
	}
	if !strings.Contains(db.commands[0], "2026-09-28T02:18:06.782000Z") {
		t.Errorf("the clock check must compare the exact target, got %q", db.commands[0])
	}
	// A commit after the target is what lets recovery see it was reached; it
	// must commit before the switch, so it lands in the archived segment.
	marker := strings.Index(db.commands[1], "txid_current()")
	switched := strings.Index(db.commands[1], "pg_switch_wal()")
	if marker < 0 || switched < marker || strings.Count(db.commands[1], " -c ") != 2 {
		t.Errorf("want a separate marker commit before the switch, got %q", db.commands[1])
	}
}

func TestArchiveGuardRefusesATargetTheSourceHasNotReachedYet(t *testing.T) {
	db := &sourceDatabase{MockClient: k8s.NewMockClient(), targetPassed: "f"}
	runningSource(db.MockClient, "src-postgres-1", nil)
	db.PodReady["org-src/src-postgres-1"] = true

	err := newTestArchiveGuard(db, time.Minute).EnsureRecoverable(context.Background(), sourceInstance(), drillTarget)
	if !errors.Is(err, ErrRestoreTargetInFuture) {
		t.Fatalf("err: got %v, want ErrRestoreTargetInFuture", err)
	}
	if len(db.commands) != 1 {
		t.Errorf("nothing may be written on the source for a refused target, got %v", db.commands)
	}
}

func TestArchiveGuardRefusesWhenTheTargetNeverReachesTheArchive(t *testing.T) {
	db := &sourceDatabase{MockClient: k8s.NewMockClient(), targetPassed: "t", segment: "000000010000000000000006",
		archived: []string{"000000010000000000000005"}}
	runningSource(db.MockClient, "src-postgres-1", nil)
	db.PodReady["org-src/src-postgres-1"] = true

	err := newTestArchiveGuard(db, 10*time.Second).EnsureRecoverable(context.Background(), sourceInstance(), drillTarget)
	if !errors.Is(err, ErrRestoreTargetNotArchived) {
		t.Fatalf("err: got %v, want ErrRestoreTargetNotArchived", err)
	}
}

func TestArchiveGuardDoesNotTakeAHistoryFileForASegment(t *testing.T) {
	db := &sourceDatabase{MockClient: k8s.NewMockClient(), targetPassed: "t", segment: "000000020000000000000006",
		archived: []string{"00000002.history"}}
	runningSource(db.MockClient, "src-postgres-1", nil)
	db.PodReady["org-src/src-postgres-1"] = true

	err := newTestArchiveGuard(db, 5*time.Second).EnsureRecoverable(context.Background(), sourceInstance(), drillTarget)
	if !errors.Is(err, ErrRestoreTargetNotArchived) {
		t.Fatalf("err: got %v, want ErrRestoreTargetNotArchived", err)
	}
}

func TestArchiveGuardRefusesWhenTheRunningSourceCannotBeAsked(t *testing.T) {
	db := &sourceDatabase{MockClient: k8s.NewMockClient(), execErr: errors.New("exec refused")}
	runningSource(db.MockClient, "src-postgres-1", nil)
	db.PodReady["org-src/src-postgres-1"] = true

	err := newTestArchiveGuard(db, time.Minute).EnsureRecoverable(context.Background(), sourceInstance(), drillTarget)
	if err == nil {
		t.Fatal("a running source that cannot be asked proves nothing; the restore must be refused")
	}
}

// A source that is not serving cannot archive anything more: recovery is the
// judge, and it fails the restore if it ends before the target.
func TestArchiveGuardLeavesAStoppedSourceToRecovery(t *testing.T) {
	cases := map[string]func(*k8s.MockClient){
		"hibernated": func(m *k8s.MockClient) {
			runningSource(m, "src-postgres-1", map[string]string{"cnpg.io/hibernation": "on"})
		},
		"no primary":        func(m *k8s.MockClient) { runningSource(m, "", nil) },
		"primary not ready": func(m *k8s.MockClient) { runningSource(m, "src-postgres-1", nil) },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			db := &sourceDatabase{MockClient: k8s.NewMockClient()}
			setup(db.MockClient)
			guard := newTestArchiveGuard(db, time.Minute)
			if err := guard.EnsureRecoverable(context.Background(), sourceInstance(), drillTarget); err != nil {
				t.Fatalf("EnsureRecoverable: %v", err)
			}
			if len(db.commands) != 0 {
				t.Errorf("a stopped source must not be exec'd into, got %v", db.commands)
			}
			future := guard.now().Add(time.Hour)
			if err := guard.EnsureRecoverable(context.Background(), sourceInstance(), future); !errors.Is(err, ErrRestoreTargetInFuture) {
				t.Errorf("a target after now: got %v, want ErrRestoreTargetInFuture", err)
			}
		})
	}
}

func TestArchiveGuardRefusesWhenTheSourceClusterCannotBeRead(t *testing.T) {
	db := &sourceDatabase{MockClient: k8s.NewMockClient()}
	err := newTestArchiveGuard(db, time.Minute).EnsureRecoverable(context.Background(), sourceInstance(), drillTarget)
	if err == nil {
		t.Fatal("an unreadable source cluster proves nothing; the restore must be refused")
	}
}

type recordingGuard struct {
	targets []time.Time
	err     error
}

func (g *recordingGuard) EnsureRecoverable(_ context.Context, _ *domain.DatabaseInstance, target time.Time) error {
	g.targets = append(g.targets, target)
	return g.err
}

func TestK8sPointInTimeRestoreIsRefusedBeforeAnythingIsCreated(t *testing.T) {
	mock := k8s.NewMockClient()
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})
	guard := &recordingGuard{err: ErrRestoreTargetNotArchived}
	adapter.SetRestoreTargetGuard(guard)

	_, err := adapter.Restore(context.Background(), sourceInstance(),
		domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst", TargetTime: &domain.ZonedTime{Time: drillTarget}})
	if !errors.Is(err, ErrRestoreTargetNotArchived) {
		t.Fatalf("err: got %v, want ErrRestoreTargetNotArchived", err)
	}
	if len(guard.targets) != 1 || !guard.targets[0].Equal(drillTarget) {
		t.Errorf("the guard must be asked about the exact target, got %v", guard.targets)
	}
	if len(mock.Namespaces) != 0 || len(mock.Secrets) != 0 || len(mock.CRDs) != 0 {
		t.Errorf("nothing may be created: ns=%v secrets=%v crds=%v", mock.Namespaces, mock.Secrets, mock.CRDs)
	}
}

func TestK8sRestoreWithoutATimeTargetDoesNotAskTheGuard(t *testing.T) {
	mock := k8s.NewMockClient()
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})
	guard := &recordingGuard{err: errors.New("must not be asked")}
	adapter.SetRestoreTargetGuard(guard)

	if _, err := adapter.Restore(context.Background(), sourceInstance(), domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(guard.targets) != 0 {
		t.Errorf("a latest restore has no target to prove, got %v", guard.targets)
	}
}

func TestK8sPointInTimeRestoreWithoutAGuardIsRefused(t *testing.T) {
	mock := k8s.NewMockClient()
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})
	adapter.SetRestoreTargetGuard(nil)

	_, err := adapter.Restore(context.Background(), sourceInstance(),
		domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst", TargetTime: &domain.ZonedTime{Time: drillTarget}})
	if !errors.Is(err, ErrRestoreTargetGuardNotConfigured) {
		t.Fatalf("err: got %v, want ErrRestoreTargetGuardNotConfigured", err)
	}
}

func TestArchiveGuardRefusesAnAnswerItCannotRead(t *testing.T) {
	db := &sourceDatabase{MockClient: k8s.NewMockClient(), targetPassed: "t", segment: "not-a-segment"}
	runningSource(db.MockClient, "src-postgres-1", nil)
	db.PodReady["org-src/src-postgres-1"] = true

	err := newTestArchiveGuard(db, time.Minute).EnsureRecoverable(context.Background(), sourceInstance(), drillTarget)
	if err == nil || errors.Is(err, ErrRestoreTargetNotArchived) {
		t.Fatalf("an unreadable WAL switch answer must refuse on its own, got %v", err)
	}
}

// failingArchiverRead answers the clock check and the switch, then cannot
// read pg_stat_archiver.
type failingArchiverRead struct{ *sourceDatabase }

func (f failingArchiverRead) ExecInPod(ctx context.Context, namespace, pod, container string, cmd []string) (string, error) {
	if strings.Contains(strings.Join(cmd, " "), "pg_stat_archiver") {
		return "", errors.New("connection lost")
	}
	return f.sourceDatabase.ExecInPod(ctx, namespace, pod, container, cmd)
}

func TestArchiveGuardRefusesWhenTheArchiverCannotBeRead(t *testing.T) {
	db := &sourceDatabase{MockClient: k8s.NewMockClient(), targetPassed: "t", segment: "000000010000000000000006"}
	runningSource(db.MockClient, "src-postgres-1", nil)
	db.PodReady["org-src/src-postgres-1"] = true
	guard := NewArchivedWALGuard(failingArchiverRead{db})
	guard.poller = steppingPoller(time.Minute)

	err := guard.EnsureRecoverable(context.Background(), sourceInstance(), drillTarget)
	if err == nil || errors.Is(err, ErrRestoreTargetNotArchived) {
		t.Fatalf("an unreadable archiver must refuse with its own reason, got %v", err)
	}
}
