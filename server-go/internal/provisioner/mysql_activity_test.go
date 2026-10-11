package provisioner

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// EXC-588: the client work of a project is summed over every instance, since
// reads may go to replicas, from performance_schema, excluding MOCO's and the
// platform's own users.
func TestMySQLClientWork_SumsEveryInstance(t *testing.T) {
	mock, prov := newMySQLTest(t)
	if _, err := prov.ProvisionWithRollback(context.Background(), mysqlRequest("8.4.9"), mysqlStandard, recordingContext()); err != nil {
		t.Fatalf("provision: %v", err)
	}
	mock.ExecCommands = nil
	mock.ExecOutput[mysqlTestNS+"/moco-proj-my1-mysql-0"] = "40\n"
	mock.ExecOutput[mysqlTestNS+"/moco-proj-my1-mysql-1"] = "2\n"
	mock.ExecOutput[mysqlTestNS+"/moco-proj-my1-mysql-2"] = "0\n"

	work, err := prov.ClientWork(context.Background(), mysqlTestNS, mysqlTestProject)
	if err != nil || work != 42 {
		t.Fatalf("work %d %v", work, err)
	}
	queries := execsMatching(mock, "performance_schema")
	if len(queries) != 3 {
		t.Fatalf("queries %v", mock.ExecCommands)
	}
	for _, excluded := range []string{"'moco-%'", "'mysql.%'", "'" + MySQLUserWatcher + "'"} {
		if !strings.Contains(queries[0], excluded) {
			t.Errorf("platform user %s is counted: %s", excluded, queries[0])
		}
	}
}

// An instance that cannot be read, or answers nonsense, fails the reading.
func TestMySQLClientWork_NeedsEveryInstance(t *testing.T) {
	for name, set := range map[string]func(map[string]string, map[string]error){
		"unreachable": func(_ map[string]string, errs map[string]error) {
			errs[mysqlTestNS+"/moco-proj-my1-mysql-0"] = errors.New("container not running")
		},
		"not a number": func(out map[string]string, _ map[string]error) {
			out[mysqlTestNS+"/moco-proj-my1-mysql-0"] = "ERROR 1142\n"
		},
	} {
		t.Run(name, func(t *testing.T) {
			mock, prov := newMySQLTest(t)
			if _, err := prov.ProvisionWithRollback(context.Background(), mysqlRequest("8.4.9"), mysqlFree, recordingContext()); err != nil {
				t.Fatalf("provision: %v", err)
			}
			if mock.ExecError == nil {
				mock.ExecError = map[string]error{}
			}
			set(mock.ExecOutput, mock.ExecError)
			if _, err := prov.ClientWork(context.Background(), mysqlTestNS, mysqlTestProject); err == nil {
				t.Fatal("a reading without every instance")
			}
		})
	}
}
