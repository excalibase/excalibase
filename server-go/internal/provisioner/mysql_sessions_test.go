package provisioner

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// EXC-675: a rotated MySQL password ends the old sessions on the primary and,
// once each has applied the change, on every replica.

const testGTID = "3e11fa47-71ca-11e1-9e33-c80aa9429562:1-77"

func mocoWithReplicas(t *testing.T) (*mysqlSessionRecorder, *MySQLProvisioner) {
	t.Helper()
	mock, prov := newMySQLTest(t)
	applyTestCluster(t, mock, mysqlStandard)
	_ = unstructured.SetNestedField(mock.CRDs[mysqlClusterKey].Object, int64(1), "status", "currentPrimaryIndex")
	recorder := &mysqlSessionRecorder{answers: map[string]string{}}
	mock.ExecAnswerInPod = func(pod string, _ []string) (string, error) {
		stdin := mock.ExecStdin[len(mock.ExecStdin)-1]
		recorder.seen = append(recorder.seen, pod+": "+stdin)
		switch {
		case strings.Contains(stdin, "gtid_executed"):
			return testGTID + "\n", nil
		case strings.Contains(stdin, "WAIT_FOR_EXECUTED_GTID_SET"):
			return recorder.waitAnswer(pod), nil
		case strings.Contains(stdin, "PROCESSLIST WHERE USER"):
			return "11\n12\n", nil
		}
		return "", nil
	}
	return recorder, prov
}

type mysqlSessionRecorder struct {
	seen    []string
	answers map[string]string
}

func (r *mysqlSessionRecorder) waitAnswer(pod string) string {
	if answer, ok := r.answers[pod]; ok {
		return answer
	}
	return "0\n"
}

func (r *mysqlSessionRecorder) on(pod, fragment string) bool {
	for _, line := range r.seen {
		if strings.HasPrefix(line, pod+": ") && strings.Contains(line, fragment) {
			return true
		}
	}
	return false
}

func TestMOCOEndUserSessionsReachesEveryCopy(t *testing.T) {
	recorder, prov := mocoWithReplicas(t)
	if err := prov.EndUserSessions(context.Background(), mysqlTestNS, mysqlTestProject, MySQLUserAdmin); err != nil {
		t.Fatalf("EndUserSessions: %v", err)
	}
	primary := "moco-proj-my1-mysql-1"
	if !recorder.on(primary, "KILL 11") || !recorder.on(primary, "KILL 12") {
		t.Errorf("the primary's sessions were not ended: %v", recorder.seen)
	}
	if recorder.on(primary, "WAIT_FOR_EXECUTED_GTID_SET") {
		t.Error("the primary waited for itself")
	}
	for _, replica := range []string{"moco-proj-my1-mysql-0", "moco-proj-my1-mysql-2"} {
		if !recorder.on(replica, "WAIT_FOR_EXECUTED_GTID_SET('"+testGTID+"'") {
			t.Errorf("%s ended sessions without waiting for the change", replica)
		}
		if !recorder.on(replica, "KILL 11") {
			t.Errorf("%s kept its sessions", replica)
		}
	}
}

func TestMOCOEndUserSessionsReportsAReplicaBehind(t *testing.T) {
	recorder, prov := mocoWithReplicas(t)
	recorder.answers["moco-proj-my1-mysql-2"] = "1\n"
	err := prov.EndUserSessions(context.Background(), mysqlTestNS, mysqlTestProject, MySQLUserAdmin)
	if err == nil || !strings.Contains(err.Error(), "moco-proj-my1-mysql-2") {
		t.Fatalf("err = %v, want the lagging replica named", err)
	}
	if recorder.on("moco-proj-my1-mysql-2", "KILL") {
		t.Error("sessions were ended on a replica that still takes the old password")
	}
}

func TestMOCOEndUserSessionsNeedsTheCluster(t *testing.T) {
	_, prov := newMySQLTest(t)
	if err := prov.EndUserSessions(context.Background(), mysqlTestNS, mysqlTestProject, MySQLUserAdmin); err == nil {
		t.Fatal("no cluster, yet sessions were reported ended")
	}
}

func TestMySQLAwaitGTIDRefusesAnythingButASet(t *testing.T) {
	if _, err := MySQLAwaitGTIDSQL("x'); DROP USER admin; --"); err == nil {
		t.Fatal("a malformed set reached SQL")
	}
	sql, err := MySQLAwaitGTIDSQL(testGTID + ",4e11fa47-71ca-11e1-9e33-c80aa9429562:1-3")
	if err != nil || !strings.Contains(sql, ", 30)") {
		t.Fatalf("sql = %q, err = %v", sql, err)
	}
}

func TestEndMySQLSessionsListsThenKills(t *testing.T) {
	var sent []string
	query := func(_ context.Context, statements string) (string, error) {
		sent = append(sent, statements)
		if strings.Contains(statements, "PROCESSLIST WHERE USER") {
			return "7\n", nil
		}
		return "", nil
	}
	if err := endMySQLSessions(context.Background(), query, "it's"); err != nil {
		t.Fatalf("endMySQLSessions: %v", err)
	}
	if !strings.Contains(sent[0], "USER = 'it''s'") || !strings.Contains(sent[0], "ID <> CONNECTION_ID()") {
		t.Errorf("listing = %s", sent[0])
	}
	if len(sent) != 2 || !strings.Contains(sent[1], "'KILL 7'") {
		t.Errorf("kills = %v", sent)
	}
}

func TestEndMySQLSessionsWithNoneOpenKillsNothing(t *testing.T) {
	calls := 0
	query := func(context.Context, string) (string, error) { calls++; return "\n", nil }
	if err := endMySQLSessions(context.Background(), query, MySQLUserApp); err != nil || calls != 1 {
		t.Fatalf("err = %v, calls = %d", err, calls)
	}
}

func TestEndMySQLSessionsRefusesAnIDThatIsNotANumber(t *testing.T) {
	query := func(context.Context, string) (string, error) { return "7; DROP\n", nil }
	if err := endMySQLSessions(context.Background(), query, MySQLUserApp); err == nil {
		t.Fatal("a non-numeric id was accepted")
	}
	failing := func(context.Context, string) (string, error) { return "", errors.New("refused") }
	if err := endMySQLSessions(context.Background(), failing, MySQLUserApp); err == nil {
		t.Fatal("a failed listing passed")
	}
}

func TestDockerMySQLEndUserSessionsRunsInTheContainer(t *testing.T) {
	engine := newFakeEngine()
	p := newTestDockerMySQL(engine)
	result := provisionDockerMySQL(t, engine)
	engine.stdinAnswer = func(_ string, _ []string, stdin string) (string, error) {
		if strings.Contains(stdin, "PROCESSLIST WHERE USER") {
			return "5\n", nil
		}
		return "", nil
	}
	before := len(engine.stdin)
	if err := p.EndUserSessions(context.Background(), result.Namespace, "p1", MySQLUserAdmin); err != nil {
		t.Fatalf("EndUserSessions: %v", err)
	}
	if got := engine.stdin[before:]; len(got) != 2 || !strings.Contains(got[1], "'KILL 5'") {
		t.Fatalf("container statements = %v", got)
	}
}

func TestMySQLReaderIsSelectOnly(t *testing.T) {
	sql := MySQLPlatformUsersSQL("app", MySQLPasswords{Admin: "a", App: "b", AuthAdmin: "c", Watcher: "d", Reader: "r5"})
	for _, want := range []string{
		"CREATE USER IF NOT EXISTS 'excalibase_reader'@'%' IDENTIFIED BY 'r5' REQUIRE SSL;",
		"ALTER USER 'excalibase_reader'@'%' IDENTIFIED BY 'r5' REQUIRE SSL;",
		"ALTER USER 'excalibase_reader'@'%' ACCOUNT UNLOCK;",
		"GRANT SELECT, SHOW VIEW ON `app`.* TO 'excalibase_reader'@'%';",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Count(sql, "TO 'excalibase_reader'") != 1 {
		t.Errorf("the reader holds more than one grant:\n%s", sql)
	}
}

func TestMySQLWithoutAReaderLocksOneARestoreBrought(t *testing.T) {
	sql := MySQLPlatformUsersSQL("app", MySQLPasswords{Admin: "a", App: "b", AuthAdmin: "c", Watcher: "d"})
	if !strings.Contains(sql, "ALTER USER IF EXISTS 'excalibase_reader'@'%' ACCOUNT LOCK;") {
		t.Errorf("an unfiled reader stays able to sign in:\n%s", sql)
	}
	if strings.Contains(sql, "CREATE USER IF NOT EXISTS 'excalibase_reader'") {
		t.Error("a reader was created without a password")
	}
}
