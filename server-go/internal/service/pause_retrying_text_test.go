package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
)

const retryingPhrase = "we are retrying automatically"

func TestSelfHealText_FailedPauseSaysItIsRetryingWhileTheFlagIsOn(t *testing.T) {
	f := newObservedPause(t)
	f.svc.SetFeatures(features.NewStatic(features.SelfHeal))
	f.pauser.pauseErr = errors.New("pod still Terminating")

	err := f.pause(t)

	if !errors.Is(err, ErrPauseNotObserved) || !strings.Contains(err.Error(), retryingPhrase) || strings.Contains(err.Error(), "retry to continue") {
		t.Fatalf("err = %v", err)
	}
	if reason := f.reload(t).FailureReason; reason != ErrPauseRetrying.Error() {
		t.Fatalf("stored reason = %q", reason)
	}
}

func TestSelfHealText_FailedResumeSaysItIsRetryingWhileTheFlagIsOn(t *testing.T) {
	f := newObservedPause(t)
	f.svc.SetFeatures(features.NewStatic(features.SelfHeal))
	if err := f.pause(t); err != nil {
		t.Fatal(err)
	}
	f.svc.replication = &restartRecorder{log: &f.pauser.log, err: errors.New("vault sealed")}

	err := f.svc.Resume(context.Background(), observedPauseProject)

	if !errors.Is(err, ErrResumeNotObserved) || !strings.Contains(err.Error(), retryingPhrase) {
		t.Fatalf("err = %v", err)
	}
	inst := f.reload(t)
	if inst.Status != string(domain.StatusResuming) || inst.FailureReason != ErrResumeRetrying.Error() {
		t.Fatalf("status %s, reason %q", inst.Status, inst.FailureReason)
	}
}

func TestSelfHealText_FlagOffKeepsTheOldSentence(t *testing.T) {
	f := newObservedPause(t)
	f.pauser.pauseErr = errors.New("pod still Terminating")

	err := f.pause(t)

	if err != ErrPauseNotObserved || f.reload(t).FailureReason != ErrPauseNotObserved.Error() {
		t.Fatalf("err = %v, reason %q", err, f.reload(t).FailureReason)
	}
}

func TestSelfHealText_CarriesNoInternals(t *testing.T) {
	for _, err := range []error{ErrPauseRetrying, ErrResumeRetrying} {
		for _, leak := range []string{"namespace", "cnpg", "vault", "pod/", "helm"} {
			if strings.Contains(strings.ToLower(err.Error()), leak) {
				t.Errorf("%v leaks %q", err, leak)
			}
		}
	}
}
