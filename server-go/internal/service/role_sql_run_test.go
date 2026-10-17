package service

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type scriptedExecer struct {
	calls   [][]string
	outputs []string
	errs    []error
}

func (e *scriptedExecer) exec(_ context.Context, cmd []string) (string, error) {
	e.calls = append(e.calls, cmd)
	i := len(e.calls) - 1
	out, err := "", error(nil)
	if i < len(e.outputs) {
		out = e.outputs[i]
	}
	if i < len(e.errs) {
		err = e.errs[i]
	}
	return out, err
}

func isProbe(cmd []string) bool {
	return strings.Contains(strings.Join(cmd, " "), "server_version_num")
}

func isTerminate(cmd []string) bool {
	return strings.Contains(cmd[len(cmd)-1], "pg_terminate_backend")
}

// A lock timeout is the only failure that warrants terminating backends and
// retrying; the terminate lands between the attempts.
func TestRunSuperuserRoleSQL_TerminatesOnLockTimeoutThenRetries(t *testing.T) {
	lockErr := errors.New(`psql exit 3: ERROR:  canceling statement due to lock timeout`)
	e := &scriptedExecer{
		// probe, attempt 1 (lock timeout), terminate, attempt 2 (ok)
		outputs: []string{"160004", "", "", ""},
		errs:    []error{nil, lockErr, nil, nil},
	}
	if err := runSuperuserRoleSQL(context.Background(), e.exec, "app", "SELECT 1;"); err != nil {
		t.Fatalf("expected success on retry, got %v", err)
	}
	if len(e.calls) != 4 {
		t.Fatalf("calls = %d, want probe + attempt + terminate + retry", len(e.calls))
	}
	if !isProbe(e.calls[0]) || !isTerminate(e.calls[2]) {
		t.Fatalf("call order wrong: probe=%v terminate=%v", e.calls[0], e.calls[2])
	}
}

// A failure that is not a lock timeout terminates nothing and is returned at
// once — no customer backend is killed for an unrelated error.
func TestRunSuperuserRoleSQL_DoesNotTerminateOnOtherFailures(t *testing.T) {
	other := errors.New("psql exit 1: ERROR:  syntax error")
	e := &scriptedExecer{outputs: []string{"160004", ""}, errs: []error{nil, other}}
	if err := runSuperuserRoleSQL(context.Background(), e.exec, "app", "SELECT 1;"); err == nil {
		t.Fatal("expected the error to surface")
	}
	for _, c := range e.calls {
		if isTerminate(c) {
			t.Fatal("a non-lock-timeout failure terminated backends")
		}
	}
}
