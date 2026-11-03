package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// k8sUpgradeWorld is a CNPG cluster on Postgres 16 whose operator the test
// plays: react runs on every wait of the upgrader.
type k8sUpgradeWorld struct {
	mock     *k8s.MockClient
	upgrader *K8sMajorUpgrader
	inst     *domain.DatabaseInstance
	now      time.Time
	react    func(w *k8sUpgradeWorld)
	progress *recordingProgress
	backups  *fakeSafetyBackups
}

type recordingProgress struct {
	steps      []string
	stopped    *time.Time
	converted  *time.Time
	convertErr error
}

func (p *recordingProgress) Step(step string)           { p.steps = append(p.steps, step) }
func (p *recordingProgress) WritesStopped(at time.Time) { p.stopped = &at }
func (p *recordingProgress) Converted(at time.Time) error {
	if p.convertErr != nil {
		return p.convertErr
	}
	p.converted = &at
	return nil
}

type fakeSafetyBackups struct {
	ids []string
	err error
}

func (f *fakeSafetyBackups) TakeSafetyBackup(context.Context, *domain.DatabaseInstance) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	id := "backup-" + string(rune('a'+len(f.ids)))
	f.ids = append(f.ids, id)
	return id, nil
}

const upgradeCluster = "org-src/src-postgres"

func newK8sUpgradeWorld(t *testing.T) *k8sUpgradeWorld {
	t.Helper()
	config.PublishPostgresCatalogForTest()
	mock := k8s.NewMockClient()
	image16, _ := config.PostgresImage("16")
	cluster := k8s.BuildPostgreSQLCluster(k8s.PostgreSQLClusterOpts{
		ProjectID: "src", Namespace: "org-src", ImageName: image16,
		Backup: &k8s.BackupOpts{Schedule: "0 0 * * *", RetentionDays: 7, Bucket: "bkt", SecretName: k8s.BackupCredentialsSecretName},
	})
	mock.CRDs[upgradeCluster] = cluster
	store, err := k8s.BuildBackupObjectStore("src", "org-src", k8s.ObjectStoreOpts{Bucket: "bkt", SecretName: k8s.BackupCredentialsSecretName}, 7)
	if err != nil {
		t.Fatal(err)
	}
	mock.CRDs["org-src/src-backups"] = store
	w := &k8sUpgradeWorld{
		mock: mock, now: time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC),
		inst:     &domain.DatabaseInstance{ProjectID: "src", OrgID: "org", Namespace: "org-src", PostgresVersion: "16", Status: "UPGRADING"},
		progress: &recordingProgress{},
		backups:  &fakeSafetyBackups{},
	}
	w.serving(16, 2, 2)
	w.upgrader = NewK8sMajorUpgrader(K8sMajorUpgraderConfig{
		Client:  mock,
		Backups: w.backups,
		Now:     func() time.Time { return w.now },
		Sleep: func(ctx context.Context, d time.Duration) error {
			w.now = w.now.Add(d)
			if w.react != nil {
				w.react(w)
			}
			return ctx.Err()
		},
	})
	return w
}

func (w *k8sUpgradeWorld) cluster() *unstructured.Unstructured { return w.mock.CRDs[upgradeCluster] }

func (w *k8sUpgradeWorld) serving(major int64, instances, ready int64) {
	status := map[string]interface{}{
		"phase": "Cluster in healthy state", "currentPrimary": "src-postgres-1",
		"instances": instances, "readyInstances": ready,
		"pgDataImageInfo": map[string]interface{}{"majorVersion": major},
	}
	_ = unstructured.SetNestedMap(w.cluster().Object, status, "status")
}

func (w *k8sUpgradeWorld) upgrading() {
	_ = unstructured.SetNestedField(w.cluster().Object, "Upgrading Postgres major version", "status", "phase")
	_ = unstructured.SetNestedField(w.cluster().Object, int64(0), "status", "readyInstances")
}

func (w *k8sUpgradeWorld) job(failed bool) {
	job := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]interface{}{"name": "src-postgres-1-major-upgrade", "namespace": "org-src",
			"labels": map[string]interface{}{"cnpg.io/jobRole": "major-upgrade", "cnpg.io/cluster": "src-postgres"}},
	}}
	job.SetCreationTimestamp(metav1.NewTime(w.now))
	if failed {
		_ = unstructured.SetNestedSlice(job.Object, []interface{}{map[string]interface{}{"type": "Failed", "status": "True"}}, "status", "conditions")
	}
	w.mock.CRDs["org-src/src-postgres-1-major-upgrade"] = job
}

func (w *k8sUpgradeWorld) image() string {
	image, _, _ := unstructured.NestedString(w.cluster().Object, "spec", "imageName")
	return image
}

func (w *k8sUpgradeWorld) run() domain.MajorUpgrade {
	return domain.MajorUpgrade{ID: "mu", ProjectID: "src", FromVersion: "16", ToVersion: "17", Status: domain.MajorUpgradeRunning}
}

func (w *k8sUpgradeWorld) imageIs(major string) bool {
	image, _ := config.PostgresImage(major)
	return w.image() == image
}

func TestK8sConvertMovesImageAndArchiveInOneUpdate(t *testing.T) {
	w := newK8sUpgradeWorld(t)
	w.react = func(w *k8sUpgradeWorld) {
		if w.imageIs("17") {
			w.serving(17, 2, 0)
		}
	}
	outcome := w.upgrader.Convert(context.Background(), w.inst, w.run(), w.progress)
	if !outcome.Converted || outcome.RolledBack {
		t.Fatalf("outcome = %+v", outcome)
	}
	store := w.mock.CRDs["org-src/src-backups-pg17"]
	if store == nil {
		t.Fatal("no store for the new major's archive")
	}
	destination, _, _ := unstructured.NestedString(store.Object, "spec", "configuration", "destinationPath")
	name, server, _ := k8s.ClusterArchive(w.cluster())
	if destination != "s3://bkt/src/cloud" || name != "src-backups-pg17" || server != "pg17" || !w.imageIs("17") {
		t.Fatalf("destination %q archive %s/%s image %s", destination, name, server, w.image())
	}
	if w.progress.stopped == nil || w.progress.converted == nil {
		t.Fatalf("progress = %+v", w.progress)
	}
	if w.mock.CRDs["org-src/src-backups"] == nil {
		t.Fatal("the old archive's store was removed")
	}
}

func TestK8sConvertPutsTheOldMajorBackWhenTheJobFails(t *testing.T) {
	w := newK8sUpgradeWorld(t)
	w.react = func(w *k8sUpgradeWorld) {
		switch {
		case w.imageIs("17"):
			w.upgrading()
			w.job(true)
		case w.imageIs("16"):
			delete(w.mock.CRDs, "org-src/src-postgres-1-major-upgrade")
			w.serving(16, 2, 2)
		}
	}
	outcome := w.upgrader.Convert(context.Background(), w.inst, w.run(), w.progress)
	if !outcome.RolledBack || outcome.Converted {
		t.Fatalf("outcome = %+v", outcome)
	}
	name, server, _ := k8s.ClusterArchive(w.cluster())
	if !w.imageIs("16") || name != "src-backups" || server != "cloud" {
		t.Fatalf("archive %s/%s image %s", name, server, w.image())
	}
	if w.progress.converted != nil {
		t.Fatal("a failed conversion was recorded as converted")
	}
}

func TestK8sConvertPutsTheOldMajorBackWhenNoJobAppears(t *testing.T) {
	w := newK8sUpgradeWorld(t)
	w.react = func(w *k8sUpgradeWorld) {
		if w.imageIs("16") {
			w.serving(16, 2, 2)
		} else {
			w.upgrading()
		}
	}
	outcome := w.upgrader.Convert(context.Background(), w.inst, w.run(), w.progress)
	if !outcome.RolledBack || !w.imageIs("16") {
		t.Fatalf("outcome = %+v image %s", outcome, w.image())
	}
}

func TestK8sConvertLeavesAJobThatRunsTooLongAlone(t *testing.T) {
	w := newK8sUpgradeWorld(t)
	w.react = func(w *k8sUpgradeWorld) {
		w.upgrading()
		w.job(false)
	}
	outcome := w.upgrader.Convert(context.Background(), w.inst, w.run(), w.progress)
	if !outcome.Unsettled || outcome.Converted || outcome.RolledBack || outcome.Public == "" {
		t.Fatalf("outcome = %+v", outcome)
	}
	if !w.imageIs("17") {
		t.Fatal("a running pg_upgrade was interrupted")
	}
}

// A cluster that cannot be read must not keep the watch going forever.
func TestK8sConvertStopsWatchingAClusterItCannotRead(t *testing.T) {
	w := newK8sUpgradeWorld(t)
	var hidden *unstructured.Unstructured
	w.react = func(w *k8sUpgradeWorld) {
		if cluster := w.mock.CRDs[upgradeCluster]; cluster != nil && w.imageIs("17") {
			hidden = cluster
			delete(w.mock.CRDs, upgradeCluster)
		}
	}
	outcome := w.upgrader.Convert(context.Background(), w.inst, w.run(), w.progress)
	if !outcome.Unsettled || hidden == nil {
		t.Fatalf("outcome = %+v", outcome)
	}
}

// A failed write whose result cannot be read back is watched, not reported
// as nothing changed: the operator may be converting already.
func TestK8sConvertWatchesAWriteItCannotConfirm(t *testing.T) {
	w := newK8sUpgradeWorld(t)
	w.mock.UpdateCRDError = errors.New("timeout")
	var hidden *unstructured.Unstructured
	sleeps := 0
	w.react = func(w *k8sUpgradeWorld) {
		sleeps++
		if sleeps == 1 {
			hidden = w.mock.CRDs[upgradeCluster]
			delete(w.mock.CRDs, upgradeCluster)
			return
		}
		if hidden != nil {
			image17, _ := config.PostgresImage("17")
			_ = unstructured.SetNestedField(hidden.Object, image17, "spec", "imageName")
			w.mock.CRDs[upgradeCluster] = hidden
			hidden = nil
			w.serving(17, 2, 2)
		}
	}
	outcome := w.upgrader.Convert(context.Background(), w.inst, w.run(), w.progress)
	if !outcome.Converted {
		t.Fatalf("a write that landed unseen was not followed: %+v", outcome)
	}
}

// A failed write the read-back shows did not land changed nothing.
func TestK8sConvertReportsAWriteThatDidNotLand(t *testing.T) {
	w := newK8sUpgradeWorld(t)
	w.mock.UpdateCRDError = errors.New("refused")
	outcome := w.upgrader.Convert(context.Background(), w.inst, w.run(), w.progress)
	if !outcome.RolledBack || !strings.Contains(outcome.Public, "nothing was changed") || !w.imageIs("16") {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestK8sConvertFailsWhenTheOldMajorDoesNotComeBack(t *testing.T) {
	w := newK8sUpgradeWorld(t)
	w.react = func(w *k8sUpgradeWorld) {
		w.upgrading()
		if w.imageIs("17") {
			w.job(true)
		}
	}
	outcome := w.upgrader.Convert(context.Background(), w.inst, w.run(), w.progress)
	if outcome.Converted || outcome.RolledBack {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestK8sConvertDoesNotPutBackDataItCouldNotRecord(t *testing.T) {
	w := newK8sUpgradeWorld(t)
	w.progress.convertErr = errors.New("database down")
	w.react = func(w *k8sUpgradeWorld) {
		if w.imageIs("17") {
			w.serving(17, 2, 2)
		}
	}
	outcome := w.upgrader.Convert(context.Background(), w.inst, w.run(), w.progress)
	if outcome.Converted || outcome.RolledBack || !w.imageIs("17") {
		t.Fatalf("outcome = %+v image %s", outcome, w.image())
	}
}

func TestK8sConvertRefusesAClusterWithoutAnArchive(t *testing.T) {
	w := newK8sUpgradeWorld(t)
	image16, _ := config.PostgresImage("16")
	w.mock.CRDs[upgradeCluster] = k8s.BuildPostgreSQLCluster(k8s.PostgreSQLClusterOpts{ProjectID: "src", Namespace: "org-src", ImageName: image16})
	outcome := w.upgrader.Convert(context.Background(), w.inst, w.run(), w.progress)
	if !outcome.RolledBack || !w.imageIs("16") {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestK8sRecover(t *testing.T) {
	t.Run("nothing was changed", func(t *testing.T) {
		w := newK8sUpgradeWorld(t)
		if outcome := w.upgrader.Recover(context.Background(), w.inst, w.run(), w.progress); !outcome.RolledBack {
			t.Fatalf("outcome = %+v", outcome)
		}
	})
	t.Run("the data is already on the new major", func(t *testing.T) {
		w := newK8sUpgradeWorld(t)
		image17, _ := config.PostgresImage("17")
		_ = k8s.SetClusterArchive(w.cluster(), image17, "src-backups-pg17", "pg17")
		w.serving(17, 2, 2)
		if outcome := w.upgrader.Recover(context.Background(), w.inst, w.run(), w.progress); !outcome.Converted || w.progress.converted == nil {
			t.Fatalf("outcome = %+v", outcome)
		}
	})
	t.Run("the conversion is still running", func(t *testing.T) {
		w := newK8sUpgradeWorld(t)
		image17, _ := config.PostgresImage("17")
		_ = k8s.SetClusterArchive(w.cluster(), image17, "src-backups-pg17", "pg17")
		w.upgrading()
		w.job(false)
		ticks := 0
		w.react = func(w *k8sUpgradeWorld) {
			if ticks++; ticks == 3 {
				w.serving(17, 2, 1)
			}
		}
		if outcome := w.upgrader.Recover(context.Background(), w.inst, w.run(), w.progress); !outcome.Converted {
			t.Fatalf("outcome = %+v", outcome)
		}
	})
}

func TestK8sPreflightReadsTheDatabase(t *testing.T) {
	w := newK8sUpgradeWorld(t)
	w.mock.ExecAnswerInPod = func(pod string, cmd []string) (string, error) {
		if pod != "src-postgres-1" {
			return "", errors.New("not the primary")
		}
		return "prepared\x1f1\next\x1fapp\x1fplpgsql\n" + preflightDoneMarker + "\n", nil
	}
	blockers, err := w.upgrader.Preflight(context.Background(), w.inst, "17")
	if err != nil || len(blockers) != 1 || !strings.Contains(blockers[0], "prepared") {
		t.Fatalf("blockers = %v, %v", blockers, err)
	}
	if len(w.mock.ExecStdin) == 0 || !strings.Contains(w.mock.ExecStdin[0], "pg_prepared_xacts") {
		t.Fatal("the check script was not run")
	}
}

func TestK8sPreflightThatCannotRunIsAnError(t *testing.T) {
	w := newK8sUpgradeWorld(t)
	w.mock.ExecAnswerInPod = func(string, []string) (string, error) { return "", errors.New("pod gone") }
	if _, err := w.upgrader.Preflight(context.Background(), w.inst, "17"); !errors.Is(err, ErrMajorUpgradeCheckUnavailable) {
		t.Fatalf("err = %v", err)
	}
	w.serving(16, 2, 2)
	_ = unstructured.SetNestedField(w.cluster().Object, "", "status", "currentPrimary")
	if _, err := w.upgrader.Preflight(context.Background(), w.inst, "17"); !errors.Is(err, ErrMajorUpgradeCheckUnavailable) {
		t.Fatalf("no primary: err = %v", err)
	}
}

func TestK8sPreflightBlocksAClusterThatArchivesNowhere(t *testing.T) {
	w := newK8sUpgradeWorld(t)
	image16, _ := config.PostgresImage("16")
	w.mock.CRDs[upgradeCluster] = k8s.BuildPostgreSQLCluster(k8s.PostgreSQLClusterOpts{ProjectID: "src", Namespace: "org-src", ImageName: image16})
	w.serving(16, 1, 1)
	w.mock.ExecAnswerInPod = func(string, []string) (string, error) { return preflightDoneMarker + "\n", nil }
	blockers, err := w.upgrader.Preflight(context.Background(), w.inst, "17")
	if err != nil || len(blockers) != 1 || !strings.Contains(blockers[0], "backup") {
		t.Fatalf("blockers = %v, %v", blockers, err)
	}
}

func TestK8sTakeBackupIsTheSafetyBackup(t *testing.T) {
	w := newK8sUpgradeWorld(t)
	if id, err := w.upgrader.TakeBackup(context.Background(), w.inst); err != nil || id != "backup-a" {
		t.Fatalf("id %q err %v", id, err)
	}
}

func TestK8sWaitServing(t *testing.T) {
	w := newK8sUpgradeWorld(t)
	w.serving(17, 3, 1)
	ticks := 0
	w.react = func(w *k8sUpgradeWorld) {
		if ticks++; ticks == 4 {
			w.serving(17, 3, 3)
		}
	}
	if err := w.upgrader.WaitServing(context.Background(), w.inst); err != nil {
		t.Fatal(err)
	}
	w.serving(17, 3, 1)
	w.react = nil
	if err := w.upgrader.WaitServing(context.Background(), w.inst); err == nil {
		t.Fatal("replicas that never caught up were reported serving")
	}
}

func TestK8sAfterSteps(t *testing.T) {
	w := newK8sUpgradeWorld(t)
	if warnings := w.upgrader.AfterSteps(context.Background(), w.inst); len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	script := strings.Join(w.mock.ExecStdin, "\n")
	for _, want := range []string{"ALTER EXTENSION", "vacuumdb --all --analyze-in-stages", "statement_timeout=0"} {
		if !strings.Contains(script, want) {
			t.Errorf("after steps miss %q", want)
		}
	}
	w.mock.ExecAnswerInPod = func(string, []string) (string, error) { return "", errors.New("exit 1") }
	if warnings := w.upgrader.AfterSteps(context.Background(), w.inst); len(warnings) != 1 {
		t.Fatalf("warnings = %v", warnings)
	}
}
