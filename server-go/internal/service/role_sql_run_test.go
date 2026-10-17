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

func TestProbeServerVersionNum(t *testing.T) {
	cases := []struct {
		out  string
		err  error
		want int
	}{
		{out: "170004\n", want: 170004},
		{out: "", err: errors.New("unreachable"), want: 0},
		{out: "not-a-number", want: 0},
	}
	for _, tc := range cases {
		e := &scriptedExecer{outputs: []string{tc.out}, errs: []error{tc.err}}
		if got := probeServerVersionNum(context.Background(), e.exec, "app"); got != tc.want {
			t.Errorf("probe(%q,%v) = %d, want %d", tc.out, tc.err, got, tc.want)
		}
	}
}

func TestIsLockTimeout(t *testing.T) {
	if !isLockTimeout("ERROR:  canceling statement due to lock timeout", nil) {
		t.Error("the lock-timeout message is not recognised")
	}
	if !isLockTimeout("", errors.New("psql exit 3: ... SQLSTATE 55P03")) {
		t.Error("the 55P03 SQLSTATE is not recognised")
	}
	if isLockTimeout("ERROR: syntax error", errors.New("psql exit 1")) {
		t.Error("an unrelated error was taken for a lock timeout")
	}
}

func TestRoleSQLExecerPrefersDockerThenK8s(t *testing.T) {
	docker := &fakeDockerClientForAdapter{}
	svc := NewProvisioningService(nil, nil, nil)
	svc.SetDockerClient(docker)
	if _, err := svc.roleSQLExecer("c1", "pod")(context.Background(), []string{"psql", "-c", "SELECT 1"}); err != nil {
		t.Fatal(err)
	}
	if len(docker.execs) != 1 {
		t.Fatalf("docker execs = %d, want the command through the container", len(docker.execs))
	}
}

// When every attempt still lock-times out, the error is returned, and a
// failing terminate is logged, not fatal.
func TestRunSuperuserRoleSQL_ReturnsAfterExhaustingRetries(t *testing.T) {
	lockErr := errors.New("psql exit 3: canceling statement due to lock timeout")
	termErr := errors.New("terminate failed")
	e := &scriptedExecer{
		// probe, attempt1, terminate(fail), attempt2, terminate(fail), attempt3
		outputs: []string{"150000", "", "", "", "", ""},
		errs:    []error{nil, lockErr, termErr, lockErr, termErr, lockErr},
	}
	if err := runSuperuserRoleSQL(context.Background(), e.exec, "app", "SELECT 1;"); err == nil {
		t.Fatal("expected the lock-timeout error after exhausting retries")
	}
	terminateCalls := 0
	for _, c := range e.calls {
		if isTerminate(c) {
			terminateCalls++
		}
	}
	if terminateCalls != 2 {
		t.Fatalf("terminate attempts = %d, want one between each of the 3 tries", terminateCalls)
	}
}
