package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/tenantcert"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const rotSchedulerCurrent = "projects/rot-db/credentials/excalibase_scheduler"

// seedPreSchedulerCluster is a cluster rendered before excalibase_scheduler
// existed: every other platform role has its certificate login.
func (h *rotationHarness) seedPreSchedulerCluster(t *testing.T) {
	t.Helper()
	lines := []interface{}{}
	for _, role := range []string{roleApp, roleAuthAdmin, roleWatcher} {
		lines = append(lines, "hostssl all "+role+" all cert", "host all "+role+" all reject", "host replication "+role+" all reject")
	}
	lines = append(lines, "hostssl all app all scram-sha-256")
	cluster := &unstructured.Unstructured{Object: map[string]interface{}{}}
	cluster.SetName(rotProject + postgresClusterSuffix)
	cluster.SetNamespace(rotNamespace)
	if err := unstructured.SetNestedSlice(cluster.Object, lines, "spec", "postgresql", "pg_hba"); err != nil {
		t.Fatal(err)
	}
	h.kube.CRDs[rotNamespace+"/"+rotProject+postgresClusterSuffix] = cluster
	// The database answers the auth probe "f" (EXC-628) and runs all else.
	h.kube.ExecAnswer = func(cmd []string) (string, error) {
		if strings.Contains(strings.Join(cmd, " "), probeAuthTag) {
			return probeAnswer("f"), nil
		}
		return "ALTER ROLE", nil
	}
}

func (h *rotationHarness) clusterHBA(t *testing.T) []string {
	t.Helper()
	cluster, err := h.kube.GetCRD(context.Background(), k8s.CNPGClusterGVR, rotNamespace, rotProject+postgresClusterSuffix)
	if err != nil {
		t.Fatal(err)
	}
	lines, _, _ := unstructured.NestedStringSlice(cluster.Object, "spec", "postgresql", "pg_hba")
	return lines
}

// A project registered before EXC-623 gets the registration SQL again, a
// scheduler login filed with its certificate, and that login in pg_hba.
func TestReconcileProjectRoles_BringsAnOlderProjectUpToDate(t *testing.T) {
	h := newRotationHarness(t)
	h.seedPreSchedulerCluster(t)

	report := h.svc.ReconcileProjectRoles(context.Background())
	if report.Reconciled != 1 || len(report.Failed) != 0 {
		t.Fatalf("report = %+v", report)
	}
	// The reconcile probes the project, then the server version, runs the
	// registration SQL and probes again for what the SQL should have removed.
	if len(h.kube.ExecCommands) != 4 || !strings.Contains(h.kube.ExecCommands[0], probeAuthTag) ||
		!strings.Contains(h.kube.ExecCommands[3], probeAuthTag) {
		t.Fatalf("exec calls = %d, want a probe, a version probe, the registration SQL and a probe", len(h.kube.ExecCommands))
	}
	if !strings.Contains(h.kube.ExecCommands[1], "server_version_num") {
		t.Fatalf("second exec is not the version probe: %q", h.kube.ExecCommands[1])
	}
	if len(h.kube.ExecStdin) != 1 {
		t.Fatalf("scripts on stdin = %d, want the registration SQL", len(h.kube.ExecStdin))
	}
	sqlText := h.kube.ExecStdin[0]
	for _, part := range []string{"excalibase_reserved_rebuild", "CREATE SCHEMA IF NOT EXISTS excalibase", "excalibase_scheduler_role", "excalibase.create_role", "excalibase_share_created_schema", "ALTER ROLE %I WITH LOGIN PASSWORD"} {
		if !strings.Contains(sqlText, part) {
			t.Errorf("reconcile SQL lacks %q", part)
		}
	}
	record := h.vault.data[rotSchedulerCurrent]
	if record["username"] != roleScheduler || record["password"] == "" || record["database"] != "app" {
		t.Fatalf("scheduler record = %v", record)
	}
	if _, err := tenantcert.FromRecord(record); err != nil {
		t.Fatalf("scheduler record carries no client certificate: %v", err)
	}
	if hba := h.clusterHBA(t); !slices.Contains(hba, "hostssl all excalibase_scheduler all cert") || !slices.Contains(hba, "host all excalibase_scheduler all reject") {
		t.Fatalf("pg_hba = %q", hba)
	}
}

// A reconciled project, one with its scheduler login filed and no read access
// to auth left for excalibase_app, is only probed: a second pass changes nothing.
func TestReconcileProjectRoles_LeavesAReconciledProjectAlone(t *testing.T) {
	h := newRotationHarness(t)
	h.seedPreSchedulerCluster(t)
	h.svc.ReconcileProjectRoles(context.Background())
	filed := h.vault.data[rotSchedulerCurrent]["password"]
	hba := h.clusterHBA(t)
	execsAfterFirst := len(h.kube.ExecCommands)

	report := h.svc.ReconcileProjectRoles(context.Background())
	if report.Reconciled != 0 || len(report.Failed) != 0 {
		t.Fatalf("report = %+v", report)
	}
	if len(h.kube.ExecCommands) != execsAfterFirst+1 || !strings.Contains(h.kube.ExecCommands[execsAfterFirst], probeAuthTag) {
		t.Fatalf("a second pass ran more than one probe: %d commands", len(h.kube.ExecCommands)-execsAfterFirst)
	}
	if got := h.vault.data[rotSchedulerCurrent]["password"]; got != filed {
		t.Fatal("a second pass minted a new scheduler password")
	}
	if !slices.Equal(h.clusterHBA(t), hba) {
		t.Fatal("a second pass rewrote pg_hba")
	}
}

// EXC-628: a project registered after EXC-623 but before EXC-628 still lets
// excalibase_app read auth. The probe finds it and the registration SQL, which
// now revokes that access, runs again; the filed logins are kept.
func TestReconcileProjectRoles_RevokesAuthFromAProjectWhoseAppRoleStillReadsIt(t *testing.T) {
	h := newRotationHarness(t)
	h.seedPreSchedulerCluster(t)
	h.vault.data[rotSchedulerCurrent] = map[string]string{
		"host": "h.local", "port": "5432", "database": "app", "username": roleScheduler, "password": "Filed-1",
	}
	h.kube.ExecAnswer = authProbeAnswers("t", "t", "f")

	report := h.svc.ReconcileProjectRoles(context.Background())
	if report.Reconciled != 1 || len(report.Failed) != 0 {
		t.Fatalf("report = %+v", report)
	}
	if len(h.kube.ExecCommands) != 5 || !strings.Contains(h.kube.ExecCommands[0], probeAuthTag) ||
		!strings.Contains(h.kube.ExecCommands[4], probeAuthTag) {
		t.Fatalf("exec calls = %d, want the probe, the probe under the lease, the version probe, the registration SQL and the probe again", len(h.kube.ExecCommands))
	}
	if len(h.kube.ExecStdin) != 1 || !strings.Contains(h.kube.ExecStdin[0], `REVOKE ALL ON SCHEMA auth FROM "excalibase_app", PUBLIC`) {
		t.Fatal("the registration SQL that ran does not revoke auth")
	}
	if got := h.vault.data[rotSchedulerCurrent]["password"]; got != "Filed-1" {
		t.Fatalf("the filed scheduler password changed to %q", got)
	}
}

// Access the revokes cannot take away (a grant from another grantor, say) is
// reported, not counted as reconciled.
func TestReconcileProjectRoles_ReportsAuthAccessTheSQLDidNotRemove(t *testing.T) {
	h := newRotationHarness(t)
	h.seedPreSchedulerCluster(t)
	h.vault.data[rotSchedulerCurrent] = map[string]string{"username": roleScheduler, "password": "Filed-1"}
	h.kube.ExecAnswer = authProbeAnswers("t", "t", "t")

	report := h.svc.ReconcileProjectRoles(context.Background())
	if report.Reconciled != 0 || len(report.Failed) != 1 || !strings.Contains(report.Failed[0].Error(), "still reaches auth") {
		t.Fatalf("report = %+v", report)
	}
}

// probeAnswer is the batched probe's answer: the auth access flag, the
// platform-schema flag (clean) and no stored verifiers (EXC-821).
func probeAnswer(auth string) string {
	return probeAuthTag + "\t" + auth + "\n" + probeSchemasTag + "\tf\n"
}

// authProbeAnswers answers the auth probe with each answer in turn and every
// other command with nothing.
func authProbeAnswers(answers ...string) func(cmd []string) (string, error) {
	return func(cmd []string) (string, error) {
		if !strings.Contains(strings.Join(cmd, " "), probeAuthTag) {
			return "", nil
		}
		if len(answers) == 0 {
			return "", errors.New("no probe answer left")
		}
		answer := answers[0]
		answers = answers[1:]
		return probeAnswer(answer), nil
	}
}

// A probe whose answer cannot be read is a failure, never a guess either way.
func TestReconcileProjectRoles_ReportsAnUnreadableAuthProbe(t *testing.T) {
	h := newRotationHarness(t)
	h.vault.data[rotSchedulerCurrent] = map[string]string{"username": roleScheduler, "password": "Filed-1"}
	h.kube.ExecOutput[rotPod] = "psql: something else"

	report := h.svc.ReconcileProjectRoles(context.Background())
	if report.Reconciled != 0 || len(report.Failed) != 1 || !strings.Contains(report.Failed[0].Error(), "auth") {
		t.Fatalf("report = %+v", report)
	}
	if len(h.kube.ExecCommands) != 1 {
		t.Fatalf("exec calls after an unreadable probe = %q", h.kube.ExecCommands)
	}
}

// A pass that fails part-way files nothing, so the next pass tries again.
func TestReconcileProjectRoles_RetriesAfterAFailedPass(t *testing.T) {
	h := newRotationHarness(t)
	h.seedPreSchedulerCluster(t)
	h.kube.UpdateCRDError = context.DeadlineExceeded
	if report := h.svc.ReconcileProjectRoles(context.Background()); len(report.Failed) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if _, filed := h.vault.data[rotSchedulerCurrent]; filed {
		t.Fatal("the scheduler login was filed although pg_hba never admitted it")
	}
	h.kube.UpdateCRDError = nil
	if report := h.svc.ReconcileProjectRoles(context.Background()); report.Reconciled != 1 {
		t.Fatalf("retry report = %+v", report)
	}
}

// askedLeader reports each leadership check and never leads, so a pass
// touches nothing.
type askedLeader chan struct{}

func (l askedLeader) IsLeader(context.Context) (bool, error) {
	l <- struct{}{}
	return false, nil
}

func TestStartRoleReconcile_RunsAtOnceThenOnTheTimer(t *testing.T) {
	h := newRotationHarness(t)
	asked := make(askedLeader)
	stop := h.svc.StartRoleReconcile(context.Background(), asked, 10*time.Millisecond)
	defer stop()
	for pass := 0; pass < 2; pass++ {
		select {
		case <-asked:
		case <-time.After(5 * time.Second):
			t.Fatalf("pass %d never ran", pass+1)
		}
	}
}

func TestReconcileProjectRoles_SkipsAProjectThatIsNotRunning(t *testing.T) {
	h := newRotationHarness(t)
	inst, _ := h.store.FindByProjectID(rotProject)
	inst.Status = "PAUSED"
	if err := h.store.Update(inst); err != nil {
		t.Fatal(err)
	}
	if report := h.svc.ReconcileProjectRoles(context.Background()); report.Reconciled != 0 || len(report.Failed) != 0 {
		t.Fatalf("report = %+v", report)
	}
	if len(h.kube.ExecCommands) != 0 {
		t.Fatal("a paused project's database was reached")
	}
}

func TestReconcileProjectRoles_ReportsAFailedExec(t *testing.T) {
	h := newRotationHarness(t)
	h.seedPreSchedulerCluster(t)
	h.kube.ExecError[rotPod] = context.DeadlineExceeded
	report := h.svc.ReconcileProjectRoles(context.Background())
	if report.Reconciled != 0 || len(report.Failed) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if _, filed := h.vault.data[rotSchedulerCurrent]; filed {
		t.Fatal("a scheduler credential was filed for a role the database never got")
	}
}

type reconcileLeader struct {
	leads bool
	err   error
}

func (l reconcileLeader) IsLeader(context.Context) (bool, error) { return l.leads, l.err }

func TestRunRoleReconcile_OnlyTheLeaderReconciles(t *testing.T) {
	h := newRotationHarness(t)
	h.seedPreSchedulerCluster(t)

	if report := h.svc.RunRoleReconcile(context.Background(), reconcileLeader{leads: false}); report.Reconciled != 0 {
		t.Fatalf("a follower reconciled: %+v", report)
	}
	if report := h.svc.RunRoleReconcile(context.Background(), reconcileLeader{err: context.Canceled}); report.Reconciled != 0 {
		t.Fatalf("a failed leadership check reconciled: %+v", report)
	}
	if len(h.kube.ExecCommands) != 0 {
		t.Fatal("a replica that does not lead reached a database")
	}
	if report := h.svc.RunRoleReconcile(context.Background(), reconcileLeader{leads: true}); report.Reconciled != 1 {
		t.Fatalf("the leader did not reconcile: %+v", report)
	}
}

// A project another operation holds is left for the next start.
func TestRunRoleReconcile_SkipsAProjectAnotherOperationHolds(t *testing.T) {
	h := newRotationHarness(t)
	release, claimed, err := h.svc.claimer().Claim(context.Background(), rotProject, OperationPause)
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	defer release()
	report := h.svc.RunRoleReconcile(context.Background(), reconcileLeader{leads: true})
	if report.Reconciled != 0 || len(report.Failed) != 1 || !errors.Is(report.Failed[0], ErrProjectOperationRunning) {
		t.Fatalf("report = %+v", report)
	}
}

// A single-host project gets the SQL through its container and a password
// login filed; it has no pg_hba or certificate to manage.
func TestReconcileProjectRoles_SingleHostFilesAPasswordLogin(t *testing.T) {
	h := newRotationHarness(t)
	docker := &fakeDockerClientForAdapter{}
	h.svc = NewProvisioningService(h.failing, nil, nil)
	h.svc.SetVault(h.vault)
	h.svc.SetDockerClient(docker)

	if report := h.svc.ReconcileProjectRoles(context.Background()); report.Reconciled != 1 || len(report.Failed) != 0 {
		t.Fatalf("report = %+v", report)
	}
	if len(docker.execs) != 4 || !strings.Contains(docker.stdins[2], "excalibase_scheduler_role") {
		t.Fatalf("container execs = %d", len(docker.execs))
	}
	if !strings.Contains(strings.Join(docker.execs[1], " "), "server_version_num") {
		t.Fatalf("second container exec is not the version probe: %v", docker.execs[1])
	}
	record := h.vault.data[rotSchedulerCurrent]
	if record["username"] != roleScheduler || record["password"] == "" {
		t.Fatalf("scheduler record = %v", record)
	}
	if _, err := tenantcert.FromRecord(record); err == nil {
		t.Fatal("a single-host login was given a client certificate")
	}
}

// Filing the scheduler login comes last; when vault refuses it the project is
// reported failed, so a later pass mints and files it again.
func TestReconcileProjectRoles_ReportsAFailedFiling(t *testing.T) {
	h := newRotationHarness(t)
	h.seedPreSchedulerCluster(t)
	h.vault.putErr = errors.New("vault refused")
	report := h.svc.ReconcileProjectRoles(context.Background())
	if report.Reconciled != 0 || len(report.Failed) != 1 || !strings.Contains(report.Failed[0].Error(), "excalibase_scheduler credential") {
		t.Fatalf("report = %+v", report)
	}
}

// A pg_hba the cluster refuses to take is a failure, not a silent pass.
func TestReconcileProjectRoles_ReportsACertLoginUpdateFailure(t *testing.T) {
	h := newRotationHarness(t)
	h.seedPreSchedulerCluster(t)
	h.kube.UpdateCRDError = errors.New("conflict forever")
	report := h.svc.ReconcileProjectRoles(context.Background())
	if report.Reconciled != 0 || len(report.Failed) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if _, filed := h.vault.data[rotSchedulerCurrent]; filed {
		t.Fatal("the scheduler login was filed although its pg_hba line never landed")
	}
}

// A filed record with no password is not reused: the role gets a fresh one.
func TestReconcileProjectRoles_RegeneratesAnEmptyFiledPassword(t *testing.T) {
	h := newRotationHarness(t)
	h.seedPreSchedulerCluster(t)
	h.vault.data[rotAppCurrent]["password"] = ""
	if report := h.svc.ReconcileProjectRoles(context.Background()); report.Reconciled != 1 {
		t.Fatalf("report = %+v", report)
	}
	if len(h.kube.ExecStdin) != 1 || !strings.Contains(h.kube.ExecStdin[0], "CREATE ROLE") {
		t.Fatal("the registration SQL did not run")
	}
}

// EXC-821: a schema the repair leaves for an operator is a project alert, not
// only a log line, and the alert resolves once a pass finds it clean.
func TestReconcileProjectRoles_RaisesAnAlertForASchemaLeftForAnOperatorAndResolvesIt(t *testing.T) {
	h := newRotationHarness(t)
	h.seedPreSchedulerCluster(t)
	h.vault.data[rotSchedulerCurrent] = map[string]string{"username": roleScheduler, "password": "Filed-1"}
	alerts := NewAlertingService(t.TempDir())
	h.svc.SetSchemaAlerts(alerts)
	h.kube.ExecAnswer = func(cmd []string) (string, error) {
		return probeAuthTag + "\tf\n" + probeSchemasTag + "\tt\n", nil
	}
	h.kube.ExecAnswerStdin = func(string) (string, error) {
		return "", errors.New("exit code 3: ERROR: 42501: schema auth holds a relation or a table that is " + platformSchemaLeftMarker)
	}

	report := h.svc.ReconcileProjectRoles(context.Background())

	if len(report.Failed) != 1 || !errors.Is(report.Failed[0], errPlatformSchemaLeft) {
		t.Fatalf("report = %+v", report)
	}
	open := alerts.GetActiveAlertsForProject(rotProject)
	if len(open) != 1 || open[0].Metric != platformSchemaMetric || open[0].Severity != "CRITICAL" {
		t.Fatalf("alerts = %+v", open)
	}
	h.svc.ReconcileProjectRoles(context.Background())
	if got := len(alerts.GetActiveAlertsForProject(rotProject)); got != 1 {
		t.Fatalf("a second failed pass left %d alerts, want the one", got)
	}

	h.kube.ExecAnswer = authProbeAnswers("f", "f")
	h.kube.ExecAnswerStdin = nil
	h.svc.ReconcileProjectRoles(context.Background())
	if open := alerts.GetActiveAlertsForProject(rotProject); len(open) != 0 {
		t.Fatalf("the alert stayed open after a clean pass: %+v", open)
	}
}

// A failure that is not a schema left for an operator raises no such alert.
func TestReconcileProjectRoles_AnOrdinaryFailureRaisesNoSchemaAlert(t *testing.T) {
	h := newRotationHarness(t)
	h.seedPreSchedulerCluster(t)
	alerts := NewAlertingService(t.TempDir())
	h.svc.SetSchemaAlerts(alerts)
	h.kube.ExecError[rotPod] = context.DeadlineExceeded
	h.svc.ReconcileProjectRoles(context.Background())
	if open := alerts.GetActiveAlertsForProject(rotProject); len(open) != 0 {
		t.Fatalf("alerts = %+v", open)
	}
}

// The revokes, the password repair and the trigger drops are scripts of their
// own that commit before the registration SQL, whose guards can refuse; the
// quarantine and the refusal come after it.
func TestRegistrationScripts_CommitWhatCannotRefuseBeforeWhatCan(t *testing.T) {
	scripts := registrationScripts("ROLE SQL;", "REPAIR SQL;")
	index := func(marker string) int {
		for i, script := range scripts {
			if strings.Contains(script, marker) {
				return i
			}
		}
		t.Fatalf("no script holds %q", marker)
		return -1
	}
	lockdown, repair, role, quarantine, refusal := index("excalibase_schema_lockdown"), index("REPAIR SQL"), index("ROLE SQL"),
		index("excalibase_schema_quarantine"), index("excalibase_schema_refusal")
	if !(lockdown < repair && repair < role && role < quarantine && quarantine < refusal) {
		t.Fatalf("script order = lockdown %d, repair %d, role %d, quarantine %d, refusal %d", lockdown, repair, role, quarantine, refusal)
	}
	if strings.Contains(scripts[lockdown], "RAISE EXCEPTION") {
		t.Error("the lockdown can refuse")
	}
	if !strings.Contains(scripts[quarantine], platformSchemaLeftMarker) {
		t.Error("the quarantine refuses without the marker the alert path reads")
	}
	if len(registrationScripts("ROLE SQL;", "")) != len(scripts)-1 {
		t.Error("an empty password repair still ran as a script")
	}
}

// A schema still unclean after the scripts ran, with none of them refusing,
// is reported and alerted the same way.
func TestReconcileProjectRoles_ASchemaStillUncleanAfterTheScriptsIsAlerted(t *testing.T) {
	h := newRotationHarness(t)
	h.seedPreSchedulerCluster(t)
	h.vault.data[rotSchedulerCurrent] = map[string]string{"username": roleScheduler, "password": "Filed-1"}
	alerts := NewAlertingService(t.TempDir())
	h.svc.SetSchemaAlerts(alerts)
	h.kube.ExecAnswer = func(cmd []string) (string, error) {
		return probeAuthTag + "\tf\n" + probeSchemasTag + "\tt\n", nil
	}
	report := h.svc.ReconcileProjectRoles(context.Background())
	if len(report.Failed) != 1 || !errors.Is(report.Failed[0], errPlatformSchemaLeft) {
		t.Fatalf("report = %+v", report)
	}
	if len(alerts.GetActiveAlertsForProject(rotProject)) != 1 {
		t.Fatal("no alert for a schema left unclean")
	}
}

// A repair cut off by a statement or lock timeout is a project alert, never
// only a log line.
func TestReconcileProjectRoles_ARepairCutOffByATimeoutIsAlerted(t *testing.T) {
	for name, text := range map[string]string{
		"statement timeout": "psql:<stdin>:75: ERROR:  57014: canceling statement due to statement timeout",
		"lock timeout":      "psql:<stdin>:75: ERROR:  55P03: canceling statement due to lock timeout",
		"lock code only":    "psql:<stdin>:75: ERROR:  55P03: could not obtain lock",
		"statement code":    "psql:<stdin>:75: ERROR:  57014: cancelled",
	} {
		t.Run(name, func(t *testing.T) {
			h := newRotationHarness(t)
			h.seedPreSchedulerCluster(t)
			h.vault.data[rotSchedulerCurrent] = map[string]string{"username": roleScheduler, "password": "Filed-1"}
			alerts := NewAlertingService(t.TempDir())
			h.svc.SetSchemaAlerts(alerts)
			h.kube.ExecAnswer = func(cmd []string) (string, error) {
				return probeAuthTag + "\tf\n" + probeSchemasTag + "\tt\n", nil
			}
			h.kube.ExecAnswerStdin = func(string) (string, error) { return "", errors.New("exit code 3: " + text) }
			report := h.svc.ReconcileProjectRoles(context.Background())
			if len(report.Failed) != 1 || !errors.Is(report.Failed[0], errPlatformRepairCutOff) {
				t.Fatalf("report = %+v", report)
			}
			open := alerts.GetActiveAlertsForProject(rotProject)
			if len(open) != 1 || open[0].Metric != platformSchemaMetric || !strings.Contains(open[0].Message, "timeout") {
				t.Fatalf("alerts = %+v", open)
			}
		})
	}
}

// Any failure of a repair script, whatever its error, is an alert; a failure of
// the registration SQL after them is not a schema alert, and a deadline that cut
// the repair is.
func TestReconcileProjectRoles_AnyRepairScriptFailureIsAlerted(t *testing.T) {
	for name, tc := range map[string]struct {
		text  string
		alert bool
	}{
		"lockdown":     {"exit code 3: NOTICE: excalibase-repair-stage:lockdown ERROR: 2BP01: dependent privileges exist", true},
		"quarantine":   {"exit code 3: NOTICE: excalibase-repair-stage:lockdown NOTICE: excalibase-repair-stage:registration NOTICE: excalibase-repair-stage:quarantine ERROR: 42P07: relation exists", true},
		"refusal":      {"exit code 3: NOTICE: excalibase-repair-stage:refusal ERROR: boom", true},
		"registration": {"exit code 3: NOTICE: excalibase-repair-stage:lockdown NOTICE: excalibase-repair-stage:registration ERROR: platform role(s) x were created outside the platform", false},
		"deadline":     {"context deadline exceeded", true},
	} {
		t.Run(name, func(t *testing.T) {
			h := newRotationHarness(t)
			h.seedPreSchedulerCluster(t)
			h.vault.data[rotSchedulerCurrent] = map[string]string{"username": roleScheduler, "password": "Filed-1"}
			alerts := NewAlertingService(t.TempDir())
			h.svc.SetSchemaAlerts(alerts)
			h.kube.ExecAnswer = func(cmd []string) (string, error) {
				return probeAuthTag + "\tf\n" + probeSchemasTag + "\tt\n", nil
			}
			h.kube.ExecAnswerStdin = func(string) (string, error) { return "", errors.New(tc.text) }
			h.svc.ReconcileProjectRoles(context.Background())
			if got := len(alerts.GetActiveAlertsForProject(rotProject)) == 1; got != tc.alert {
				t.Fatalf("alert raised = %v, want %v", got, tc.alert)
			}
		})
	}
}

// Every revoke the repair runs cascades, so a grant chain cannot stop it, and
// every repair script announces itself for the alert path.
func TestPlatformSchemaScripts_EveryRevokeCascadesAndEachScriptNamesItself(t *testing.T) {
	lockdown, quarantine, refusal := platformSchemaScripts()
	for name, script := range map[string]string{"lockdown": lockdown, "quarantine": quarantine, "refusal": refusal} {
		for _, line := range strings.Split(script, "\n") {
			if strings.Contains(line, "REVOKE ") && !strings.Contains(line, "CASCADE") {
				t.Errorf("the %s script has a revoke without CASCADE: %s", name, strings.TrimSpace(line))
			}
		}
		if !strings.Contains(script, "excalibase-repair-stage:"+name) {
			t.Errorf("the %s script does not name itself", name)
		}
	}
}
