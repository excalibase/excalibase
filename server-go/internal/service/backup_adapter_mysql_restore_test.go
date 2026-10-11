package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/objectcreds"
)

const (
	restoredProject = "dst"
	restoredNS      = "org1-dst"
)

// restoreReady wires what a restore into a new project needs; the plan is
// the destination organisation's Free plan.
func (lab *mysqlAdapterLab) restoreReady(t *testing.T) (*fakeRegistrar, *recordingMinter) {
	t.Helper()
	store := emptyInstanceStore(t)
	registrar := &fakeRegistrar{store: store}
	minter := &recordingMinter{}
	lab.adapter.instances = store
	lab.adapter.SetProjectRegistrar(registrar)
	lab.adapter.SetDatabaseProbe(alwaysAnswers{})
	lab.adapter.SetBackupCredentials(newTestIssuer(t, minter, lab.objects))
	lab.adapter.SetRestorePlanSource(&fakeRestorePlans{plan: RestorePlan{
		Tier:   domain.Free,
		Config: config.TierConfig{Instances: 1, StorageSize: "5Gi", CPU: "0.5", Memory: "512Mi", StatementTimeout: "15s", BackupEnabled: true},
		Backup: &domain.BackupSettings{Enabled: true, Schedule: "0 2 * * *", Retention: 7},
	}})
	return registrar, minter
}

func restoreRequest(req domain.RestoreRequest) domain.RestoreRequest {
	req.NewProjectName, req.TargetProjectID = "copy", restoredProject
	return req
}

// ADR 0041 decision 6 and the owner's decisions: a restore is a new
// MySQLCluster MOCO fills from the source's prefix at a time inside the
// window; it runs the new organisation's plan, never the source's size.
func TestMySQLRestoreIntoANewProject(t *testing.T) {
	lab := newMySQLAdapterLab(t, sourceKeys()...)
	registrar, minter := lab.restoreReady(t)
	lab.source.Tier = domain.Standard

	resp, err := lab.adapter.Restore(context.Background(), lab.source, restoreRequest(domain.RestoreRequest{TargetTime: zoned(at(7, 30))}))
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if resp.ProjectID != restoredProject || resp.Status != "ACTIVE" {
		t.Errorf("response %+v", resp)
	}
	cluster := lab.mock.CRDs[restoredNS+"/"+k8s.MySQLClusterName(restoredProject)]
	if cluster == nil {
		t.Fatalf("no restored cluster; calls %v", lab.mock.Calls)
	}
	restore, _, _ := unstructured.NestedMap(cluster.Object, "spec", "restore")
	if restore["sourceName"] != "proj1-mysql" || restore["sourceNamespace"] != mysqlSourceNS || restore["restorePoint"] != "2026-10-11T07:30:00Z" {
		t.Errorf("restore %v", restore)
	}
	if replicas, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "replicas"); replicas != 1 {
		t.Errorf("a Free copy of a Standard project runs %d instances", replicas)
	}
	sourcePrefix, ownPrefix := k8s.MOCOBackupPrefix(mysqlSourceNS, mysqlSourceProject), k8s.MOCOBackupPrefix(restoredNS, restoredProject)
	if got := string(lab.mock.Secrets[restoredNS+"/"+k8s.MySQLRestoreSourceCredentialsSecret]["AWS_ACCESS_KEY_ID"]); got != "tmp-"+sourcePrefix {
		t.Errorf("source credential for %q", got)
	}
	if got := string(lab.mock.Secrets[restoredNS+"/"+k8s.MySQLBackupCredentialsSecret]["AWS_ACCESS_KEY_ID"]); got != "tmp-"+ownPrefix {
		t.Errorf("own credential for %q", got)
	}
	scopes := minter.minted()
	if len(scopes) != 2 || scopes[0].Access != objectcreds.ReadOnly || scopes[0].Prefix != sourcePrefix {
		t.Errorf("minted %+v", scopes)
	}
	policy := lab.mock.CRDs[restoredNS+"/"+k8s.MySQLBackupPolicyName(restoredProject)]
	if schedule, _, _ := unstructured.NestedString(policy.Object, "spec", "schedule"); schedule != "0 */6 * * *" {
		t.Errorf("the copy's backups run on %q", schedule)
	}
	if name, _, _ := unstructured.NestedString(cluster.Object, "spec", "backupPolicyName"); name == "" {
		t.Error("the copy is not linked to its backup policy")
	}
	row := registrar.calls[0]
	if row.DBType != domain.MySQL || row.MySQLVersion != "8.4.9" || row.Tier != domain.Free ||
		row.Host != k8s.MySQLPrimaryHost(restoredProject, restoredNS) || row.RestoredFromProjectID != mysqlSourceProject ||
		row.BackupSchedule != "0 */6 * * *" {
		t.Errorf("registered %+v", row)
	}
}

// Outside the window nothing is created and the caller is told the window.
func TestMySQLRestoreOutsideTheWindowCreatesNothing(t *testing.T) {
	lab := newMySQLAdapterLab(t, sourceKeys()...)
	lab.restoreReady(t)
	_, err := lab.adapter.Restore(context.Background(), lab.source, restoreRequest(domain.RestoreRequest{TargetTime: zoned(at(12, 30))}))
	if !errors.Is(err, ErrRestoreTargetInvalid) || !strings.Contains(err.Error(), "backup window") {
		t.Fatalf("err %v", err)
	}
	if lab.mock.Namespaces[restoredNS] {
		t.Error("a namespace was created for a refused restore")
	}
}

// A restore that is not proved usable is compensated away.
func TestMySQLRestoreThatFailsRegistrationIsRolledBack(t *testing.T) {
	lab := newMySQLAdapterLab(t, sourceKeys()...)
	registrar, _ := lab.restoreReady(t)
	registrar.err = errors.New("vault sealed")
	_, err := lab.adapter.Restore(context.Background(), lab.source, restoreRequest(domain.RestoreRequest{}))
	if !errors.Is(err, ErrRestoreNotObserved) {
		t.Fatalf("err %v", err)
	}
	if lab.mock.Namespaces[restoredNS] {
		t.Error("the restore's namespace was left behind")
	}
}
