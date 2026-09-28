package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const recoveryPod = "dst-postgres-1-full-recovery-abcde"

func failedRecoveryPod(mock *k8s.MockClient, logTail string) {
	mock.Pods["org-dst"] = []corev1.Pod{{
		ObjectMeta: metav1.ObjectMeta{Name: recoveryPod, Namespace: "org-dst",
			Labels: map[string]string{"cnpg.io/jobRole": "full-recovery", "cnpg.io/cluster": "dst-postgres"}},
		Status: corev1.PodStatus{Phase: corev1.PodFailed},
	}}
	mock.PodLogs["org-dst/"+recoveryPod] = logTail
}

const fatalNotReached = `{"level":"info","record":{"error_severity":"FATAL","message":"recovery ended before configured recovery target was reached"}}`

// Postgres refuses to promote a recovery whose archive ended before the
// target; the restore fails on that at once, with that reason, and nothing
// it created survives.
func TestK8sRestoreFailsWhenRecoveryEndsBeforeTheTarget(t *testing.T) {
	mock := k8s.NewMockClient()
	failedRecoveryPod(mock, "starting point-in-time recovery\n"+fatalNotReached+"\n")
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})
	adapter.SetRestoreTargetGuard(&recordingGuard{})

	_, err := adapter.Restore(context.Background(), sourceInstance(),
		domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst", TargetTime: &domain.ZonedTime{Time: drillTarget}})
	if !errors.Is(err, ErrRestoreTargetNotReached) {
		t.Fatalf("err: got %v, want ErrRestoreTargetNotReached", err)
	}
	if _, ok := mock.CRDs["org-dst/dst-postgres"]; ok {
		t.Error("the cluster of a restore that missed its target must be removed")
	}
}

// A recovery pod can fail for reasons the Job retries; only Postgres saying
// the target was not reached ends the wait early.
func TestK8sRestoreKeepsWaitingOnAnotherRecoveryFailure(t *testing.T) {
	mock := k8s.NewMockClient()
	failedRecoveryPod(mock, "could not reach the object store\n")
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})

	if _, err := adapter.Restore(context.Background(), sourceInstance(), domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
}
