package service

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const srcCluster = "org-src/src-postgres"

// runningSourceCluster is the source project's live cluster, archiving to
// its own store.
func runningSourceCluster() *unstructured.Unstructured {
	obj := k8s.BuildPostgreSQLCluster(k8s.PostgreSQLClusterOpts{
		ProjectID: "src", Namespace: "org-src",
		Tier:           config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5"},
		DatabaseName:   "shop",
		MasterUsername: "owner",
		Backup:         &k8s.BackupOpts{Schedule: "0 2 * * *", RetentionDays: 7, EndpointURL: testR2Endpoint, Bucket: "excalibase-backups"},
	})
	// Round-trip through JSON: the API server hands back JSON numbers.
	raw, _ := json.Marshal(obj.Object)
	live := &unstructured.Unstructured{}
	_ = live.UnmarshalJSON(raw)
	_ = unstructured.SetNestedMap(live.Object, map[string]interface{}{"phase": "Cluster in healthy state"}, "status")
	return live
}

// operatorPoller stands in for CNPG: every tick completes the Backups that
// were asked for and reports every Cluster healthy.
func operatorPoller(mock *k8s.MockClient, backupPhase string) provisioner.Poller {
	now := time.Unix(0, 0)
	return provisioner.Poller{
		Interval: time.Second,
		Timeout:  time.Minute,
		Now:      func() time.Time { return now },
		After: func(d time.Duration) <-chan time.Time {
			now = now.Add(d)
			for key, obj := range mock.CRDs {
				switch obj.GetKind() {
				case "Backup":
					_ = unstructured.SetNestedMap(obj.Object, map[string]interface{}{
						"phase": backupPhase, "backupId": "barman-" + obj.GetName(),
					}, "status")
				case "Cluster":
					name := key[strings.Index(key, "/")+1:]
					_ = unstructured.SetNestedMap(obj.Object, map[string]interface{}{
						"phase": "Cluster in healthy state", "currentPrimary": name + "-1", "readyInstances": int64(1),
					}, "status")
				}
			}
			ch := make(chan time.Time, 1)
			ch <- now
			return ch
		},
	}
}

func inPlaceAdapter(t *testing.T, backupPhase string) (*K8sBackupAdapter, *k8s.MockClient) {
	t.Helper()
	mock := k8s.NewMockClient()
	mock.WildcardPodReady = true
	mock.CRDs[srcCluster] = runningSourceCluster()
	adapter := NewK8sBackupAdapter(mock, StaticBackupStorage(r2Storage()))
	adapter.SetReadyPoller(operatorPoller(mock, backupPhase))
	return adapter, mock
}

func backedUpSource() *domain.DatabaseInstance {
	inst := sourceInstance()
	inst.BackupEnabled = boolPtr(true)
	inst.DatabaseName, inst.Username = "shop", "owner"
	return inst
}

func TestTakeSafetyBackupWaitsForItToComplete(t *testing.T) {
	adapter, mock := inPlaceAdapter(t, "completed")

	id, err := adapter.TakeSafetyBackup(context.Background(), backedUpSource())
	if err != nil {
		t.Fatalf("TakeSafetyBackup: %v", err)
	}
	backup, ok := mock.CRDs["org-src/"+id]
	if !ok || backup.GetKind() != "Backup" {
		t.Fatalf("no Backup %q applied: %v", id, mock.Calls)
	}
	if cluster, _, _ := unstructured.NestedString(backup.Object, "spec", "cluster", "name"); cluster != "src-postgres" {
		t.Errorf("backup of %q", cluster)
	}
}

func TestTakeSafetyBackupReportsAFailedBackup(t *testing.T) {
	adapter, _ := inPlaceAdapter(t, "failed")
	if _, err := adapter.TakeSafetyBackup(context.Background(), backedUpSource()); err == nil {
		t.Fatal("a failed backup is no recovery point")
	}
}

func TestInPlaceRecoveryTargetResolvesABackupOfTheProject(t *testing.T) {
	adapter, mock := inPlaceAdapter(t, "completed")
	withSourceBackup(mock, cnpgBackup("safety", "src-postgres", "completed", "20261007T120000"))

	target, err := adapter.InPlaceRecoveryTarget(context.Background(), backedUpSource(), domain.RestoreRequest{BackupID: "safety"})
	if err != nil {
		t.Fatalf("InPlaceRecoveryTarget: %v", err)
	}
	if target["backupID"] != "20261007T120000" || target["targetImmediate"] != true {
		t.Errorf("target %v", target)
	}
}

func TestInPlaceRecoveryTargetRefusesAnUnarchivedTime(t *testing.T) {
	adapter, _ := inPlaceAdapter(t, "completed")
	adapter.SetRestoreTargetGuard(refusingGuard{ErrRestoreTargetNotArchived})
	at := &domain.ZonedTime{Time: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	_, err := adapter.InPlaceRecoveryTarget(context.Background(), backedUpSource(), domain.RestoreRequest{TargetTime: at})
	if !errors.Is(err, ErrRestoreTargetNotArchived) {
		t.Fatalf("got %v", err)
	}
}

type refusingGuard struct{ err error }

func (g refusingGuard) EnsureRecoverable(context.Context, *domain.DatabaseInstance, time.Time) error {
	return g.err
}

func TestReplaceDatabaseRecreatesTheClusterFromItsOwnArchive(t *testing.T) {
	adapter, mock := inPlaceAdapter(t, "completed")
	target := map[string]interface{}{"targetTime": "2026-10-07T12:00:00Z"}

	if err := adapter.ReplaceDatabase(context.Background(), backedUpSource(), target); err != nil {
		t.Fatalf("ReplaceDatabase: %v", err)
	}
	replaced := mock.CRDs[srcCluster]
	recovery, _, _ := unstructured.NestedMap(replaced.Object, "spec", "bootstrap", "recovery")
	if recovery["source"] != "clusterBackup" || recovery["database"] != "shop" || recovery["owner"] != "owner" {
		t.Errorf("recovery %v", recovery)
	}
	if got, _, _ := unstructured.NestedMap(recovery, "recoveryTarget"); got["targetTime"] != "2026-10-07T12:00:00Z" {
		t.Errorf("target %v", got)
	}
	external, _, _ := unstructured.NestedSlice(replaced.Object, "spec", "externalClusters")
	params, _, _ := unstructured.NestedMap(external[0].(map[string]interface{}), "plugin", "parameters")
	if params["barmanObjectName"] != k8s.BackupObjectStoreName("src") {
		t.Errorf("recovers from %v", params)
	}

	keepCA := slices.Index(mock.Calls, "KeepSecretsPastOwner:org-src/src-postgres-ca")
	keepApp := slices.Index(mock.Calls, "KeepSecretsPastOwner:org-src/src-postgres-app")
	deleted := slices.Index(mock.Calls, "DeleteCRD:"+srcCluster)
	applied := -1
	for i, call := range mock.Calls {
		if call == "ApplyCRD:"+srcCluster {
			applied = i
		}
	}
	if keepCA < 0 || keepApp < 0 || deleted < 0 || applied < 0 || keepCA > deleted || keepApp > deleted || deleted > applied {
		t.Fatalf("the CA and owner secret are kept, then the cluster is deleted, then recreated: %v", mock.Calls)
	}
	if _, left := mock.Secrets["org-src/"+inPlaceTemplateSecret("src")]; left {
		t.Error("the saved definition is removed once the database is back")
	}
}

// A replace interrupted after the cluster was deleted is retried from the
// definition it saved first.
func TestReplaceDatabaseRetriesFromTheSavedDefinition(t *testing.T) {
	adapter, mock := inPlaceAdapter(t, "completed")
	saved, _ := json.Marshal(mock.CRDs[srcCluster].Object)
	mock.Secrets["org-src/"+inPlaceTemplateSecret("src")] = map[string][]byte{inPlaceTemplateKey: saved}
	delete(mock.CRDs, srcCluster)

	if err := adapter.ReplaceDatabase(context.Background(), backedUpSource(), nil); err != nil {
		t.Fatalf("ReplaceDatabase: %v", err)
	}
	if _, ok := mock.CRDs[srcCluster]; !ok {
		t.Fatal("the cluster must be recreated from the saved definition")
	}
}

func TestReplaceDatabaseWithNothingToRebuildFromChangesNothing(t *testing.T) {
	adapter, mock := inPlaceAdapter(t, "completed")
	delete(mock.CRDs, srcCluster)

	if err := adapter.ReplaceDatabase(context.Background(), backedUpSource(), nil); err == nil {
		t.Fatal("no cluster and no saved definition: nothing to rebuild")
	}
	if slices.ContainsFunc(mock.Calls, func(c string) bool { return strings.HasPrefix(c, "ApplyCRD:") }) {
		t.Errorf("nothing may be created: %v", mock.Calls)
	}
}

func TestReplaceDatabaseWaitsForTheOldVolumesToGo(t *testing.T) {
	adapter, mock := inPlaceAdapter(t, "completed")
	mock.PVCs["org-src"] = []string{"src-postgres-1", "app-disk-1"}

	if err := adapter.ReplaceDatabase(context.Background(), backedUpSource(), nil); err == nil {
		t.Fatal("the replacement cannot take a volume name that is still in use")
	}
	if slices.Contains(mock.Calls[slices.Index(mock.Calls, "DeleteCRD:"+srcCluster):], "ApplyCRD:"+srcCluster) {
		t.Errorf("recreated over a volume still held: %v", mock.Calls)
	}
}

func TestIsClusterVolume(t *testing.T) {
	for claim, want := range map[string]bool{
		"src-postgres-1": true, "src-postgres-12-wal": true, "src-postgres-": false,
		"src-postgres-app": false, "other-postgres-1": false, "src-postgres-1-x": false,
	} {
		if got := isClusterVolume(claim, "src-postgres"); got != want {
			t.Errorf("%s: got %v", claim, got)
		}
	}
}

// Each step that fails stops the replace with its error.
func TestReplaceDatabaseStopsOnAFailedStep(t *testing.T) {
	boom := errors.New("boom")
	for name, breakIt := range map[string]func(*k8s.MockClient){
		"saving the definition": func(m *k8s.MockClient) { m.CreateSecretError = boom },
		"deleting the cluster":  func(m *k8s.MockClient) { m.DeleteCRDError = boom },
		"listing its pods":      func(m *k8s.MockClient) { m.GetPodsError = boom },
		"listing its volumes":   func(m *k8s.MockClient) { m.ListPVCsError = boom },
		"creating the cluster":  func(m *k8s.MockClient) { m.CRDError = boom },
		"the mongo service":     func(m *k8s.MockClient) { m.EnsureDocumentDBServiceError = boom },
		"a corrupt saved definition": func(m *k8s.MockClient) {
			m.Secrets["org-src/"+inPlaceTemplateSecret("src")] = map[string][]byte{inPlaceTemplateKey: []byte("{not json")}
		},
	} {
		t.Run(name, func(t *testing.T) {
			adapter, mock := inPlaceAdapter(t, "completed")
			breakIt(mock)
			inst := backedUpSource()
			inst.DocumentDB = true
			if err := adapter.ReplaceDatabase(context.Background(), inst, nil); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestInPlaceRecoveryTargetRefusesAnUnknownBackup(t *testing.T) {
	adapter, _ := inPlaceAdapter(t, "completed")
	if _, err := adapter.InPlaceRecoveryTarget(context.Background(), backedUpSource(), domain.RestoreRequest{BackupID: "nope"}); err == nil {
		t.Fatal("a backup the project does not have is no target")
	}
}

func TestReplaceDatabaseOfAClusterWithoutAnArchiveChangesNothing(t *testing.T) {
	adapter, mock := inPlaceAdapter(t, "completed")
	unstructured.RemoveNestedField(mock.CRDs[srcCluster].Object, "spec", "plugins")
	if err := adapter.ReplaceDatabase(context.Background(), backedUpSource(), nil); !errors.Is(err, k8s.ErrClusterHasNoArchive) {
		t.Fatalf("got %v", err)
	}
	if slices.Contains(mock.Calls, "DeleteCRD:"+srcCluster) {
		t.Error("a cluster that cannot be recovered is not deleted")
	}
}

func TestTakeSafetyBackupThatCannotStartIsAnError(t *testing.T) {
	adapter, mock := inPlaceAdapter(t, "completed")
	mock.CRDError = errors.New("forbidden")
	if _, err := adapter.TakeSafetyBackup(context.Background(), backedUpSource()); err == nil {
		t.Fatal("want an error")
	}
}

func TestReplaceDatabaseDoesNotDeleteWhenTheSecretsCannotBeKept(t *testing.T) {
	adapter, mock := inPlaceAdapter(t, "completed")
	mock.KeepSecretsError = errors.New("forbidden")

	if err := adapter.ReplaceDatabase(context.Background(), backedUpSource(), nil); err == nil {
		t.Fatal("want an error")
	}
	if slices.Contains(mock.Calls, "DeleteCRD:"+srcCluster) {
		t.Error("deleting the cluster would take the CA every client certificate is signed by")
	}
}
