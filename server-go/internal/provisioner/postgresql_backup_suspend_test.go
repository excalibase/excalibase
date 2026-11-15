package provisioner

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const pauseScheduleKey = pauseTestNS + "/" + pauseTestProject + "-postgres-backup"

func seedPauseSchedule(t *testing.T, mock *k8s.MockClient) {
	t.Helper()
	backup, err := k8s.BuildScheduledBackup(pauseTestProject, pauseTestNS, "0 2 * * *")
	if err != nil {
		t.Fatal(err)
	}
	mock.CRDs[pauseScheduleKey] = backup
}

func pauseScheduleSuspended(mock *k8s.MockClient) bool {
	suspended, _, _ := unstructured.NestedBool(mock.CRDs[pauseScheduleKey].Object, "spec", "suspend")
	return suspended
}

// EXC-758: a hibernated cluster cannot be backed up, so the nightly schedule
// must not fire while the project is paused.
func TestPostgreSQLPause_SuspendsTheNightlyBackup(t *testing.T) {
	mock, prov := setupHibernationTest(t)
	seedPauseSchedule(t, mock)

	if err := prov.Pause(context.Background(), pauseTestNS, pauseTestProject); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if !pauseScheduleSuspended(mock) {
		t.Fatal("the nightly backup still fires on a paused project")
	}
	assertAnnotation(t, storedCluster(t, mock), hibernationAnnotation, hibernationOn)
}

func TestPostgreSQLResume_ResumesTheNightlyBackup(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.CRDs[pauseTestClusterKey] = newHibernationTestCluster(3)
	seedPauseSchedule(t, mock)
	if err := k8s.SetScheduledBackupSuspended(context.Background(), mock, pauseTestNS, pauseTestProject, true); err != nil {
		t.Fatal(err)
	}
	client := &readyAfterClient{MockClient: mock}
	prov := NewPostgreSQLProvisioner(client, "")
	prov.resumeTimeout, prov.resumePoll = 200*1e6, 5*1e6

	if err := prov.Resume(context.Background(), pauseTestNS, pauseTestProject); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if pauseScheduleSuspended(mock) {
		t.Fatal("the schedule stayed suspended after the project resumed")
	}
}

// Setting a new schedule on a paused project must not wake it up.
func TestPostgreSQLConfigureBackup_KeepsAPausedScheduleSuspended(t *testing.T) {
	mock, prov := setupHibernationTest(t)
	seedPauseSchedule(t, mock)
	if err := k8s.SetScheduledBackupSuspended(context.Background(), mock, pauseTestNS, pauseTestProject, true); err != nil {
		t.Fatal(err)
	}

	if err := prov.ConfigureBackup(context.Background(), pauseTestNS, pauseTestProject, "0 3 * * *", 7); err != nil {
		t.Fatal(err)
	}
	if !pauseScheduleSuspended(mock) {
		t.Fatal("changing the schedule resumed the nightly backup of a paused project")
	}
}
