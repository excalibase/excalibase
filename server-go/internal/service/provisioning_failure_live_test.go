//go:build live

package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/excalibase/provisioning-poc/internal/testutil/pgstore"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

// Failed provisioning (EXC-534) on real k3s, real CloudNativePG, the real Postgres platform store and
// the real pkg/vault on that store. Only two things are injected: API failures
// of a delete (faultyKube), and the moment the real vault's Seal() is called
// (sealTimedVault). Neither replaces any behaviour.
//
// Run: go test ./internal/service/ -tags=live -run TestLiveFailedProvisioning -v -count=1 -timeout 45m

var errInjectedAPI = errors.New("etcdserver: request timed out")

type faultyKube struct {
	k8s.KubeClient
	failNamespaceDelete atomic.Bool
	failCRDDelete       atomic.Bool
}

func (k *faultyKube) DeleteNamespace(ctx context.Context, name string) error {
	if k.failNamespaceDelete.Load() {
		return errInjectedAPI
	}
	return k.KubeClient.DeleteNamespace(ctx, name)
}

func (k *faultyKube) DeleteCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error {
	if k.failCRDDelete.Load() {
		return errInjectedAPI
	}
	return k.KubeClient.DeleteCRD(ctx, gvr, namespace, name)
}

// sealTimedVault is the real vault; it only chooses when the real Seal() runs.
type sealTimedVault struct {
	*vault.Vault
	sealAfterPutSuffix atomic.Pointer[string]
	sealAfterNextCheck atomic.Bool
}

func (v *sealTimedVault) Put(path string, data map[string]string) error {
	err := v.Vault.Put(path, data)
	if suffix := v.sealAfterPutSuffix.Load(); suffix != nil && strings.HasSuffix(path, *suffix) {
		v.sealAfterPutSuffix.Store(nil)
		v.Vault.Seal()
	}
	return err
}

func (v *sealTimedVault) Sealed() bool {
	sealed := v.Vault.Sealed()
	if !sealed && v.sealAfterNextCheck.CompareAndSwap(true, false) {
		v.Vault.Seal()
	}
	return sealed
}

type failureLab struct {
	backup *backupLab
	doc    *documentDBLab
	kube   *faultyKube
	store  *postgres.Store
	vault  *sealTimedVault
	share  string
	orgs   *fakestore.Orgs
	svc    *ProvisioningService
	ctx    context.Context
}

func startFailureLab(t *testing.T) *failureLab {
	t.Helper()
	backup := &backupLab{documentDBLab: startDocumentDBLab(t), suffix: randomHex(t, 3)}
	backup.installBackupStack(t)
	doc := backup.documentDBLab
	store := pgstore.New(t)
	real, err := vault.NewWithStore(vault.NewPostgresStore(store.DB()))
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	init, err := real.Init(1, 1)
	if err != nil {
		t.Fatalf("vault init: %v", err)
	}
	lab := &failureLab{backup: backup, doc: doc, kube: &faultyKube{KubeClient: doc.client}, store: store,
		vault: &sealTimedVault{Vault: real}, share: init.Shares[0], orgs: fakestore.NewOrgs(), ctx: doc.ctx}
	if real.Sealed() {
		lab.unseal(t)
	}
	lab.svc = NewProvisioningService(store, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(lab.kube, "")), lab.kube)
	lab.svc.SetOrgStore(lab.orgs)
	lab.svc.SetVault(lab.vault)
	// The Free plan requires backups, so the real MinIO target, minted credentials and purger are wired.
	if err := lab.svc.SetBackupDefaults(&BackupDefaults{AccessKeyID: backup.store.AccessKeyID, SecretAccessKey: backup.store.SecretAccessKey,
		Endpoint: backup.store.Endpoint, Bucket: backup.store.Bucket, Region: backup.store.Region, Schedule: backupLiveSchedule, RetentionDays: 7}); err != nil {
		t.Fatalf("backup defaults: %v", err)
	}
	lab.svc.SetBackupCredentials(backup.issuer)
	lab.svc.SetBackupPurger(NewBackupPurger(StaticBackupStorage(backup.store), "backups/", AWSObjectDeleterFactory(true)))
	return lab
}

func (lab *failureLab) unseal(t *testing.T) {
	t.Helper()
	progress, err := lab.vault.Vault.Unseal(lab.share)
	if err != nil || !progress.Done {
		t.Fatalf("real unseal: err=%v", err)
	}
	if lab.vault.Vault.Sealed() {
		t.Fatal("still sealed after unseal")
	}
}

func (lab *failureLab) resetFaults(t *testing.T) {
	t.Helper()
	lab.kube.failNamespaceDelete.Store(false)
	lab.kube.failCRDDelete.Store(false)
	lab.vault.sealAfterPutSuffix.Store(nil)
	lab.vault.sealAfterNextCheck.Store(false)
	if lab.vault.Vault.Sealed() {
		lab.unseal(t)
	}
	lab.svc.rollbackRetries.now = nil
	lab.svc.rollbackRetries.next = nil
	lab.svc.buildTimeout = 0
	lab.svc.SetDeletionClock(nil)
}

func (lab *failureLab) org(name string) string {
	lab.orgs.AddOrg(name, domain.Free)
	return name
}

func failureRequest(org string) domain.ProvisioningRequest {
	return domain.ProvisioningRequest{ProjectName: "failure", OrgID: org, DBType: domain.PostgreSQL, PostgresVersion: "17"}
}

func (lab *failureLab) row(t *testing.T, id string) *domain.DatabaseInstance {
	t.Helper()
	row, err := lab.store.FindByProjectID(id)
	if err != nil {
		t.Logf("find %s: %v", id, err)
		return nil
	}
	return row
}

func (lab *failureLab) nsExists(t *testing.T, ns string) bool {
	t.Helper()
	exists, err := lab.doc.client.NamespaceExists(lab.ctx, ns)
	if err != nil {
		t.Fatalf("namespace %s: %v", ns, err)
	}
	return exists
}

func (lab *failureLab) waitNsGone(t *testing.T, ns string) {
	t.Helper()
	started := time.Now()
	eventuallyLive(t, "namespace "+ns+" gone", 8*time.Minute, func() bool { return !lab.nsExists(t, ns) })
	t.Logf("namespace %s gone after %s", ns, time.Since(started).Round(time.Second))
}

func (lab *failureLab) runningPods(t *testing.T, ns string) int {
	t.Helper()
	pods, err := lab.doc.cs.CoreV1().Pods(ns).List(lab.ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("pods of %s: %v", ns, err)
	}
	running := 0
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp == nil {
			running++
		}
	}
	return running
}

// clusterAnswers proves the database serves and the platform's app role was made.
func (lab *failureLab) clusterAnswers(t *testing.T, id, ns string) {
	t.Helper()
	out, err := lab.doc.client.ExecInPod(lab.ctx, ns, id+"-postgres-1", "postgres",
		[]string{"psql", "-U", "postgres", "-tAc", "SELECT count(*) FROM pg_roles WHERE rolname='excalibase_app'"})
	if err != nil || strings.TrimSpace(out) != "1" {
		t.Fatalf("database of %s: role count %q err %v", id, out, err)
	}
}

func (lab *failureLab) vaultPaths(t *testing.T, id string) []string {
	t.Helper()
	paths, err := lab.vault.Vault.List(vaultProjectPrefix(id))
	if err != nil {
		t.Fatalf("list vault: %v", err)
	}
	return paths
}

// archivedObjects counts what the project has in the real object store.
func (lab *failureLab) archivedObjects(t *testing.T, id string) int {
	t.Helper()
	deleter, err := AWSObjectDeleterFactory(true)(lab.ctx, lab.backup.store)
	if err != nil {
		t.Fatalf("object store: %v", err)
	}
	keys, _, err := deleter.ListKeys(lab.ctx, lab.backup.store.Bucket, k8s.BarmanObjectPrefix(id), "", 1000)
	if err != nil {
		t.Fatalf("list objects: %v", err)
	}
	return len(keys)
}

func logRollback(t *testing.T, row *domain.DatabaseInstance) {
	t.Helper()
	for _, result := range parseRollbackLog(row.RollbackLog) {
		t.Logf("  rollback: %q ok=%t err=%q scoped=%t", result.Name, result.OK, result.Error, result.ForFailure != "")
	}
}

func rollbackFailed(row *domain.DatabaseInstance, name string) bool {
	for _, result := range parseRollbackLog(row.RollbackLog) {
		if result.Name == name && !result.OK {
			return true
		}
	}
	return false
}

// removeProject hard-deletes through the service and proves everything went.
func (lab *failureLab) removeProject(t *testing.T, id string) {
	t.Helper()
	row := lab.row(t, id)
	if row == nil {
		return
	}
	if isProtected(row) && !domain.IsDeletionStatus(row.Status) {
		if err := lab.svc.SetDeletionProtection(id, false); err != nil {
			t.Errorf("turn protection off: %v", err)
		}
	}
	if err := lab.svc.DeprovisionWithOptions(lab.ctx, id, DeprovisionOptions{}); err != nil {
		t.Errorf("deprovision %s: %v", id, err)
		return
	}
	if got := lab.row(t, id); got != nil {
		t.Errorf("row %s left as %s", id, got.Status)
	}
	if lab.nsExists(t, row.Namespace) {
		t.Errorf("namespace %s left", row.Namespace)
	}
	if left := lab.vaultPaths(t, id); len(left) != 0 {
		t.Errorf("%d vault secrets left under %s", len(left), id)
	}
}

// activeProject builds a healthy project with the vault open.
func (lab *failureLab) activeProject(t *testing.T, org string) (string, string) {
	t.Helper()
	resp, err := lab.svc.Provision(lab.ctx, failureRequest(lab.org(org)))
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if resp.Status != string(domain.StatusActive) {
		row := lab.row(t, resp.ProjectID)
		logRollback(t, row)
		t.Fatalf("status %s: %s", resp.Status, row.FailureReason)
	}
	return resp.ProjectID, resp.Namespace
}

func TestLiveFailedProvisioning(t *testing.T) {
	lab := startFailureLab(t)
	cases := []struct {
		name string
		run  func(*testing.T, *failureLab)
	}{
		{"dropped connection: the build finishes and exec honours deadlines", failureHangUp},
		{"build deadline: rollback runs on a dead context", failureDeadline},
		{"sealed from the start: FAILED cleanly, after unseal delete and create work", failureSealedFromStart},
		{"sealed and rollback fails: sweep finishes it, unseal finishes the rest", failureSealedRollbackFails},
		{"sealed mid registration on a create", failureSealedMidCreate},
		{"sealed mid registration on an add, retried after unseal", failureSealedMidAdd},
		{"failed restore copy waits for the vault, unseal finishes it", failureRestoreWaitsForVault},
		{"final delete stopped by a seal resumes after unseal", failureRestoreDeleteStoppedBySeal},
		{"failed add: sweep removes the cluster and a retry succeeds", failureFailedAddSwept},
		{"failed add deleted: the cluster goes now, backups stay for the grace period", failureFailedAddDeleted},
		{"old log without forFailure is never swept", failureLegacyLog},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lab.resetFaults(t)
			defer lab.resetFaults(t)
			tc.run(t, lab)
		})
	}
}

func failureHangUp(t *testing.T, lab *failureLab) {
	// A real HTTP connection the client closes: net/http cancels r.Context().
	type outcome struct {
		resp *domain.ProvisioningResponse
		err  error
	}
	done := make(chan outcome, 1)
	entered := make(chan struct{})
	req := failureRequest(lab.org("hangup"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		resp, err := lab.svc.Provision(r.Context(), req)
		done <- outcome{resp, err}
		_ = r.Context().Err()
	}))
	defer server.Close()
	clientCtx, hangUp := context.WithCancel(lab.ctx)
	defer hangUp()
	request, _ := http.NewRequestWithContext(clientCtx, http.MethodPost, server.URL, nil)
	go func() {
		if response, err := http.DefaultClient.Do(request); err == nil {
			response.Body.Close()
		}
	}()
	<-entered
	time.Sleep(5 * time.Second)
	hangUp()
	t.Log("client hung up 5s into the build")
	started := time.Now()
	result := <-done
	if result.err != nil {
		t.Fatalf("provision: %v", result.err)
	}
	t.Logf("build finished %s after the hang-up: %s", time.Since(started).Round(time.Second), result.resp.Status)
	row := lab.row(t, result.resp.ProjectID)
	if row.Status != string(domain.StatusActive) {
		logRollback(t, row)
		t.Fatalf("row is %s: %s", row.Status, row.FailureReason)
	}
	lab.clusterAnswers(t, row.ProjectID, row.Namespace)
	if paths := lab.vaultPaths(t, row.ProjectID); len(paths) == 0 {
		t.Fatal("nothing filed in the real vault")
	} else {
		t.Logf("real vault holds %d secrets for the project", len(paths))
	}

	// The exec transport on a real API server and kubelet.
	pod := row.ProjectID + "-postgres-1"
	out, err := lab.doc.client.ExecInPod(lab.ctx, row.Namespace, pod, "postgres", []string{"echo", "exec-ok"})
	if err != nil || strings.TrimSpace(out) != "exec-ok" {
		t.Errorf("plain exec: %q %v", out, err)
	}
	out, err = lab.doc.client.ExecInPodStdin(lab.ctx, row.Namespace, pod, "postgres", []string{"cat"}, "stdin-ok")
	if err != nil || strings.TrimSpace(out) != "stdin-ok" {
		t.Errorf("stdin exec: %q %v", out, err)
	}
	if _, err = lab.doc.client.ExecInPod(lab.ctx, row.Namespace, pod, "postgres", []string{"sh", "-c", "echo bad >&2; exit 3"}); err == nil || !strings.Contains(err.Error(), "bad") {
		t.Errorf("failing exec must carry stderr: %v", err)
	}
	short, cancel := context.WithTimeout(lab.ctx, 3*time.Second)
	defer cancel()
	began := time.Now()
	_, err = lab.doc.client.ExecInPod(short, row.Namespace, pod, "postgres", []string{"sleep", "120"})
	took := time.Since(began)
	t.Logf("exec of sleep 120 under a 3s deadline returned after %s: %v", took.Round(100*time.Millisecond), err)
	if !errors.Is(err, context.DeadlineExceeded) || took > 10*time.Second {
		t.Errorf("deadline ignored: took %s err %v", took, err)
	}
	lab.removeProject(t, row.ProjectID)
}

func failureDeadline(t *testing.T, lab *failureLab) {
	lab.svc.buildTimeout = 8 * time.Second
	started := time.Now()
	resp, err := lab.svc.Provision(lab.ctx, failureRequest(lab.org("deadline")))
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	t.Logf("returned %s after %s", resp.Status, time.Since(started).Round(time.Second))
	row := lab.row(t, resp.ProjectID)
	logRollback(t, row)
	if row.Status != string(domain.StageFailed) || !strings.Contains(row.FailureReason, "did not finish within") {
		t.Errorf("row %s: %q", row.Status, row.FailureReason)
	}
	lab.waitNsGone(t, row.Namespace)
	lab.svc.buildTimeout = 0
	lab.removeProject(t, row.ProjectID)
}

func failureSealedFromStart(t *testing.T, lab *failureLab) {
	org := lab.org("sealed-start")
	lab.vault.Vault.Seal()
	started := time.Now()
	resp, err := lab.svc.Provision(lab.ctx, failureRequest(org))
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	t.Logf("create against a sealed vault answered %s after %s (it does not wait for the vault)", resp.Status, time.Since(started).Round(time.Second))
	row := lab.row(t, resp.ProjectID)
	logRollback(t, row)
	t.Logf("reason: %s", row.FailureReason)
	if row.Status != string(domain.StageFailed) {
		t.Fatalf("row is %s", row.Status)
	}
	lab.waitNsGone(t, row.Namespace)
	lab.svc.RetryFailedRollbacks(lab.ctx)

	lab.unseal(t)
	lab.svc.RetryFailedRollbacks(lab.ctx)
	after := lab.row(t, row.ProjectID)
	t.Logf("after unseal and a sweep the row is still %s: nothing resumes the create", after.Status)
	if after.Status != string(domain.StageFailed) {
		t.Errorf("row moved to %s on its own", after.Status)
	}
	_, retryErr := lab.svc.Provision(lab.ctx, failureRequest(org))
	var limit *OrgProjectLimitError
	t.Logf("retry with the FAILED row present: slot-held=%t err=%v", errors.As(retryErr, &limit), retryErr)

	scheduled, err := lab.svc.ScheduleDeletion(lab.ctx, row.ProjectID, DeprovisionOptions{})
	if err != nil || scheduled != nil {
		t.Fatalf("DELETE of the FAILED row: %v %+v", err, scheduled)
	}
	if lab.row(t, row.ProjectID) != nil {
		t.Fatal("row left after DELETE")
	}
	id, ns := lab.activeProject(t, org)
	lab.clusterAnswers(t, id, ns)
	t.Log("after unseal, DELETE then a new create reached ACTIVE and the database answers")
	lab.removeProject(t, id)
}

func failureSealedRollbackFails(t *testing.T, lab *failureLab) {
	lab.vault.Vault.Seal()
	lab.kube.failNamespaceDelete.Store(true)
	resp, err := lab.svc.Provision(lab.ctx, failureRequest(lab.org("sealed-leftover")))
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	row := lab.row(t, resp.ProjectID)
	logRollback(t, row)
	cleanup := deleteNamespaceCleanup(row.Namespace)
	if row.Status != string(domain.StageFailed) || !rollbackFailed(row, cleanup) {
		t.Fatalf("want FAILED with a failed namespace cleanup, row %s", row.Status)
	}
	if !lab.nsExists(t, row.Namespace) || lab.runningPods(t, row.Namespace) == 0 {
		t.Fatalf("the test needs a live leftover cluster (pods %d)", lab.runningPods(t, row.Namespace))
	}
	t.Logf("leftover: %d running pods in %s, row FAILED", lab.runningPods(t, row.Namespace), row.Namespace)

	// Still failing: recorded, backed off, nothing lost.
	if finished := lab.svc.RetryFailedRollbacks(lab.ctx); len(finished) != 0 {
		t.Errorf("finished %v while the delete fails", finished)
	}
	lab.kube.failNamespaceDelete.Store(false)
	passRetryWindow(lab.svc)
	finished := lab.svc.RetryFailedRollbacks(lab.ctx)
	t.Logf("sweep with the vault still sealed finished %v", finished)
	lab.waitNsGone(t, row.Namespace)
	row = lab.row(t, row.ProjectID)
	logRollback(t, row)
	if rollbackFailed(row, cleanup) {
		t.Error("the sweep did not record its success")
	}
	lab.unseal(t)
	lab.removeProject(t, row.ProjectID)
}

func failureSealedMidCreate(t *testing.T, lab *failureLab) {
	suffix := "/" + roleAdmin
	lab.vault.sealAfterPutSuffix.Store(&suffix)
	resp, err := lab.svc.Provision(lab.ctx, failureRequest(lab.org("sealed-mid")))
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	row := lab.row(t, resp.ProjectID)
	logRollback(t, row)
	t.Logf("status %s reason %s", row.Status, row.FailureReason)
	if row.Status != string(domain.StageFailed) {
		t.Fatalf("row is %s", row.Status)
	}
	lab.waitNsGone(t, row.Namespace)
	lab.unseal(t)
	lab.svc.RetryFailedRollbacks(lab.ctx)
	t.Logf("after unseal and a sweep the real vault still holds %d secret(s) for the FAILED project", len(lab.vaultPaths(t, row.ProjectID)))
	lab.removeProject(t, row.ProjectID)
}

func failureSealedMidAdd(t *testing.T, lab *failureLab) {
	created, err := lab.svc.Provision(lab.ctx, domain.ProvisioningRequest{ProjectName: "failure", OrgID: lab.org("sealed-add"), NoDatabase: true})
	if err != nil || created.Status != string(domain.StatusActive) {
		t.Fatalf("create without a database: %v %+v", err, created)
	}
	id, ns := created.ProjectID, created.Namespace
	defer lab.removeProject(t, id)
	suffix := "/" + roleAdmin
	lab.vault.sealAfterPutSuffix.Store(&suffix)
	add := domain.ProvisioningRequest{DBType: domain.PostgreSQL, PostgresVersion: "17"}
	if _, err := lab.svc.AddDatabase(lab.ctx, id, add); err != nil {
		t.Fatalf("add: %v", err)
	}
	row := lab.row(t, id)
	logRollback(t, row)
	t.Logf("first add: status %s nodb=%t reason %s", row.Status, row.NoDatabase, row.FailureReason)
	eventuallyLive(t, "failed add's pods gone", 5*time.Minute, func() bool { return lab.runningPods(t, ns) == 0 })

	lab.unseal(t)
	passRetryWindow(lab.svc)
	lab.svc.RetryFailedRollbacks(lab.ctx)
	t.Logf("after unseal and a sweep the real vault holds %d secret(s)", len(lab.vaultPaths(t, id)))
	started := time.Now()
	if _, err := lab.svc.AddDatabase(lab.ctx, id, add); err != nil {
		t.Fatalf("retry after unseal: %v", err)
	}
	row = lab.row(t, id)
	logRollback(t, row)
	t.Logf("retry after unseal took %s: status %s nodb=%t reason %q", time.Since(started).Round(time.Second), row.Status, row.NoDatabase, row.FailureReason)
	if row.NoDatabase || row.Status != string(domain.StatusActive) {
		t.Errorf("the add cannot be retried after the vault opens: %q", row.FailureReason)
	}
}

// failedRestoreFromRealProject writes the row through failRestore, the code a
// failed restore runs, over a real cluster with real secrets in the real vault.
func (lab *failureLab) failedRestoreFromRealProject(t *testing.T, org string) *domain.DatabaseInstance {
	t.Helper()
	id, ns := lab.activeProject(t, org)
	if _, err := lab.store.DB().Exec(`UPDATE database_instances SET restored_from_project_id = 'src-failure' WHERE project_id = $1`, id); err != nil {
		t.Fatalf("mark as a copy: %v", err)
	}
	pc := provisioner.NewProvisionContext(nil, nil)
	pc.RegisterCleanup(restoreNamespaceCleanup(ns), func(context.Context) error { return errInjectedAPI })
	pc.RegisterLastCleanup(unverifiedRowCleanup, func(context.Context) error { return lab.store.Delete(id) })
	record := lab.row(t, id)
	if err := failRestore(lab.ctx, pc, lab.store, record, errors.New("probe failed")); !errors.Is(err, ErrRestoreNotObserved) {
		t.Fatalf("failRestore: %v", err)
	}
	row := lab.row(t, id)
	if row == nil || row.Status != string(domain.StageFailed) || !isFailedRestore(row) {
		t.Fatalf("want the failed-restore row, got %+v", row)
	}
	logRollback(t, row)
	return row
}

func failureRestoreWaitsForVault(t *testing.T, lab *failureLab) {
	row := lab.failedRestoreFromRealProject(t, "restore-wait")
	id, ns := row.ProjectID, row.Namespace
	held := len(lab.vaultPaths(t, id))
	lab.vault.Vault.Seal()

	eventuallyLive(t, "namespace removed while sealed", 8*time.Minute, func() bool {
		if finished := lab.svc.RetryFailedRollbacks(lab.ctx); len(finished) != 0 {
			t.Fatalf("finished %v while sealed", finished)
		}
		return !lab.nsExists(t, ns)
	})
	t.Logf("namespace gone while sealed; row %s; real vault holds %d secrets", lab.row(t, id).Status, held)
	for pass := 0; pass < 3; pass++ {
		lab.svc.RetryFailedRollbacks(lab.ctx)
		if got := lab.row(t, id); got == nil || got.Status != string(domain.StageFailed) {
			t.Fatalf("pass %d while sealed: %+v", pass, got)
		}
		if !lab.svc.rollbackRetries.due(id) {
			t.Fatal("a sealed vault was penalised with backoff")
		}
	}
	lab.unseal(t)
	if len(lab.vaultPaths(t, id)) != held {
		t.Fatal("secrets changed while sealed")
	}
	finished := lab.svc.RetryFailedRollbacks(lab.ctx)
	t.Logf("first sweep after unseal finished %v", finished)
	if lab.row(t, id) != nil {
		t.Fatalf("row left: %+v", lab.row(t, id).Status)
	}
	if left := lab.vaultPaths(t, id); len(left) != 0 {
		t.Fatalf("%d secrets left in the real vault", len(left))
	}
	t.Log("row gone, namespace gone, real vault prefix empty")
}

func failureRestoreDeleteStoppedBySeal(t *testing.T, lab *failureLab) {
	row := lab.failedRestoreFromRealProject(t, "restore-stop")
	id, ns := row.ProjectID, row.Namespace
	eventuallyLive(t, "namespace removed", 8*time.Minute, func() bool {
		lab.vault.sealAfterNextCheck.Store(false)
		if lab.nsExists(t, ns) {
			lab.svc.RetryFailedRollbacks(lab.ctx)
			return false
		}
		return true
	})
	if got := lab.row(t, id); got == nil {
		t.Fatal("the sweep finished before the test could seal")
	}
	// The guard reads open; the real seal lands before the delete's vault step.
	lab.vault.sealAfterNextCheck.Store(true)
	lab.svc.RetryFailedRollbacks(lab.ctx)
	stopped := lab.row(t, id)
	if stopped == nil || stopped.Status != string(domain.StatusDeleting) {
		t.Fatalf("want DELETING, got %+v", stopped)
	}
	t.Logf("delete stopped: status %s step %q err %q reason %q", stopped.Status, stopped.DeletionStep, stopped.DeletionError, stopped.FailureReason)
	passRetryWindow(lab.svc)
	lab.svc.RetryFailedRollbacks(lab.ctx)
	if got := lab.row(t, id); got == nil || got.Status != string(domain.StatusDeleting) {
		t.Fatalf("while sealed: %+v", got)
	}
	lab.unseal(t)
	finished := lab.svc.RetryFailedRollbacks(lab.ctx)
	t.Logf("sweep after unseal finished %v", finished)
	if got := lab.row(t, id); got != nil {
		t.Fatalf("row left: %s step %s: %s", got.Status, got.DeletionStep, got.DeletionError)
	}
	if left := lab.vaultPaths(t, id); len(left) != 0 {
		t.Fatalf("%d secrets left", len(left))
	}
}

// failedAddWithLiveCluster fails an add at the sealed vault gate with the removal failing too.
func (lab *failureLab) failedAddWithLiveCluster(t *testing.T, org string) *domain.DatabaseInstance {
	t.Helper()
	created, err := lab.svc.Provision(lab.ctx, domain.ProvisioningRequest{ProjectName: "failure", OrgID: lab.org(org), NoDatabase: true})
	if err != nil {
		t.Fatalf("create without a database: %v", err)
	}
	lab.vault.Vault.Seal()
	lab.kube.failCRDDelete.Store(true)
	if _, err := lab.svc.AddDatabase(lab.ctx, created.ProjectID, domain.ProvisioningRequest{DBType: domain.PostgreSQL, PostgresVersion: "17"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	lab.kube.failCRDDelete.Store(false)
	lab.unseal(t)
	row := lab.row(t, created.ProjectID)
	logRollback(t, row)
	if row.Status != string(domain.StatusActive) || !row.NoDatabase || !rollbackFailed(row, removeDatabaseCleanup(row.Namespace)) {
		t.Fatalf("want ACTIVE without a database and a failed removal: %s nodb=%t", row.Status, row.NoDatabase)
	}
	if lab.runningPods(t, row.Namespace) == 0 {
		t.Fatal("the test needs the failed add's cluster running")
	}
	t.Logf("failed add's cluster running: %d pods in %s, reason %q, %d archived objects", lab.runningPods(t, row.Namespace), row.Namespace, row.FailureReason, lab.archivedObjects(t, row.ProjectID))
	return row
}

func failureFailedAddSwept(t *testing.T, lab *failureLab) {
	row := lab.failedAddWithLiveCluster(t, "add-swept")
	id, ns := row.ProjectID, row.Namespace
	defer lab.removeProject(t, id)
	add := domain.ProvisioningRequest{DBType: domain.PostgreSQL, PostgresVersion: "17"}
	if _, err := lab.svc.AddDatabase(lab.ctx, id, add); !errors.Is(err, ErrProjectOperationRunning) {
		t.Errorf("a new add over a standing cluster must be refused (409), got %v", err)
	} else {
		t.Logf("new add refused: %v", err)
	}
	started := time.Now()
	eventuallyLive(t, "sweep finishes the removal", 10*time.Minute, func() bool {
		passRetryWindow(lab.svc)
		lab.svc.RetryFailedRollbacks(lab.ctx)
		return !rollbackFailed(lab.row(t, id), removeDatabaseCleanup(ns))
	})
	t.Logf("sweep removed the cluster in %s, %d pods left", time.Since(started).Round(time.Second), lab.runningPods(t, ns))
	if pods := lab.runningPods(t, ns); pods != 0 {
		t.Errorf("%d pods still running", pods)
	}
	swept := lab.row(t, id)
	t.Logf("after the sweep: reason %q, %d archived objects", swept.FailureReason, lab.archivedObjects(t, id))
	if strings.Contains(swept.FailureReason, purgeWaitingNote) {
		t.Errorf("the purge still waits")
	}
	if _, err := lab.svc.AddDatabase(lab.ctx, id, add); err != nil {
		t.Fatalf("retry: %v", err)
	}
	row = lab.row(t, id)
	if row.Status != string(domain.StatusActive) || row.NoDatabase {
		logRollback(t, row)
		t.Fatalf("retry: %s nodb=%t: %s", row.Status, row.NoDatabase, row.FailureReason)
	}
	lab.clusterAnswers(t, id, ns)
	t.Log("retry reached ACTIVE and the database answers")
}

func failureFailedAddDeleted(t *testing.T, lab *failureLab) {
	row := lab.failedAddWithLiveCluster(t, "add-deleted")
	id, ns := row.ProjectID, row.Namespace
	if err := lab.svc.SetDeletionProtection(id, false); err != nil {
		t.Fatalf("turn protection off, as a caller must first: %v", err)
	}
	archived := lab.archivedObjects(t, id)
	scheduled, err := lab.svc.ScheduleDeletion(lab.ctx, id, DeprovisionOptions{})
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if scheduled.Status != string(domain.StatusPendingDeletion) {
		t.Errorf("DELETE answered %s", scheduled.Status)
	}
	eventuallyLive(t, "the failed add's pods gone during the grace period", 5*time.Minute, func() bool { return lab.runningPods(t, ns) == 0 })
	if got := lab.archivedObjects(t, id); got < archived {
		t.Errorf("%d archived objects became %d: backups are the grace period's", archived, got)
	}
	lab.svc.SetDeletionClock(func() time.Time { return time.Now().Add(DeletionGracePeriod + time.Hour) })
	report := lab.svc.RunDueDeletions(lab.ctx)
	t.Logf("after the grace period: deleted %v failed %v", report.Deleted, report.Failed)
	if lab.row(t, id) != nil || lab.nsExists(t, ns) {
		t.Errorf("not removed after the grace period")
	}
}

func failureLegacyLog(t *testing.T, lab *failureLab) {
	org := lab.org("legacy")
	id := "legacyrow"
	ns := org + "-" + id
	if err := lab.kube.CreateProjectNamespace(lab.ctx, ns, org); err != nil {
		t.Fatalf("namespace: %v", err)
	}
	row := &domain.DatabaseInstance{ProjectID: id, ProjectName: id, OrgID: org, Namespace: ns, DBType: domain.PostgreSQL,
		DeploymentMode: domain.ModeK8s, Status: string(domain.StageFailed), CurrentStage: domain.StageFailed, FailureReason: "old failure",
		RollbackLog: `[{"name":"` + deleteNamespaceCleanup(ns) + `","ok":false,"error":"timed out"}]`}
	if err := lab.store.Create(row); err != nil {
		t.Fatalf("row: %v", err)
	}
	passRetryWindow(lab.svc)
	lab.svc.RetryFailedRollbacks(lab.ctx)
	time.Sleep(10 * time.Second)
	if !lab.nsExists(t, ns) {
		t.Error("the sweep acted on a log that does not name the failure it was for")
	}
	lab.removeProject(t, id)
}
