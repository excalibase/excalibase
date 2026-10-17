package k8s

import (
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const testMOCOBackupImage = "ghcr.io/cybozu-go/moco-backup:0.37.0"

func egressWaitOf(t *testing.T, cronJob *unstructured.Unstructured) map[string]interface{} {
	t.Helper()
	inits, _, _ := unstructured.NestedSlice(cronJob.Object, mocoJobPodSpecPath("initContainers")...)
	for _, raw := range inits {
		if container, ok := raw.(map[string]interface{}); ok && container["name"] == MySQLEgressWaitContainerName {
			return container
		}
	}
	t.Fatalf("no %s init container in %v", MySQLEgressWaitContainerName, inits)
	return nil
}

func envOf(container map[string]interface{}) map[string]string {
	env := map[string]string{}
	list, _ := container["env"].([]interface{})
	for _, raw := range list {
		entry, _ := raw.(map[string]interface{})
		name, _ := entry["name"].(string)
		value, _ := entry["value"].(string)
		env[name] = value
	}
	return env
}

// EXC-586: kube-router admits a new pod to its peers' policies a moment after
// it starts and moco-backup dials at once, so the backup Job's pod first
// waits, in MOCO's own image, until the store and the database answer it.
func TestWithMySQLBackupEgressWait(t *testing.T) {
	cron := MockMOCOBackupCronJob("org-proj-abc", "proj-abc-mysql", testMOCOBackupImage, "http://rustfs.platform.svc:9000")
	waited, changed, err := WithMySQLBackupEgressWait(cron)
	if err != nil || !changed {
		t.Fatalf("WithMySQLBackupEgressWait: changed=%v err=%v", changed, err)
	}
	if _, found, _ := unstructured.NestedSlice(cron.Object, mocoJobPodSpecPath("initContainers")...); found {
		t.Error("the CronJob passed in was changed")
	}
	wait := egressWaitOf(t, waited)
	if wait["image"] != testMOCOBackupImage {
		t.Errorf("image %v, want the backup container's", wait["image"])
	}
	env := envOf(wait)
	if env[mysqlEgressWaitStoreEnv] != "rustfs.platform.svc:9000" {
		t.Errorf("store %q", env[mysqlEgressWaitStoreEnv])
	}
	if env[mysqlEgressWaitDatabaseEnv] != "moco-proj-abc-mysql.org-proj-abc.svc:3306" {
		t.Errorf("database %q", env[mysqlEgressWaitDatabaseEnv])
	}
	security, _ := wait["securityContext"].(map[string]interface{})
	if security["readOnlyRootFilesystem"] != true || security["allowPrivilegeEscalation"] != false || security["runAsNonRoot"] != true {
		t.Errorf("securityContext %v", security)
	}
	// Restricted PSA is warned in project namespaces; the wait itself meets it.
	if seccomp, _ := security["seccompProfile"].(map[string]interface{}); seccomp["type"] != "RuntimeDefault" {
		t.Errorf("seccompProfile %v", security["seccompProfile"])
	}
	if _, ok := wait["resources"].(map[string]interface{})["limits"]; !ok {
		t.Errorf("resources %v carry no limits", wait["resources"])
	}
	containers, _, _ := unstructured.NestedSlice(waited.Object, mocoJobPodSpecPath("containers")...)
	if len(containers) != 1 {
		t.Errorf("containers %v, want MOCO's backup container untouched", containers)
	}
}

// A CronJob that already waits is left alone, so a sweep writes nothing.
func TestWithMySQLBackupEgressWait_Idempotent(t *testing.T) {
	cron := MockMOCOBackupCronJob("org-proj-abc", "proj-abc-mysql", testMOCOBackupImage, "https://s3.example")
	once, _, err := WithMySQLBackupEgressWait(cron)
	if err != nil {
		t.Fatal(err)
	}
	twice, changed, err := WithMySQLBackupEgressWait(once)
	if err != nil || changed {
		t.Fatalf("second pass changed=%v err=%v", changed, err)
	}
	inits, _, _ := unstructured.NestedSlice(twice.Object, mocoJobPodSpecPath("initContainers")...)
	if len(inits) != 1 {
		t.Errorf("init containers %v", inits)
	}
	if env := envOf(egressWaitOf(t, twice)); env[mysqlEgressWaitStoreEnv] != "s3.example:443" {
		t.Errorf("https store without a port: %q", env[mysqlEgressWaitStoreEnv])
	}
}

// An upgraded MOCO image or a moved store refreshes the wait in place.
func TestWithMySQLBackupEgressWait_RefreshesAStaleWait(t *testing.T) {
	old, _, err := WithMySQLBackupEgressWait(MockMOCOBackupCronJob("ns", "c-mysql", "moco-backup:old", "http://old:9000"))
	if err != nil {
		t.Fatal(err)
	}
	upgraded := MockMOCOBackupCronJob("ns", "c-mysql", testMOCOBackupImage, "http://new:9000")
	inits, _, _ := unstructured.NestedSlice(old.Object, mocoJobPodSpecPath("initContainers")...)
	if err := unstructured.SetNestedSlice(upgraded.Object, inits, mocoJobPodSpecPath("initContainers")...); err != nil {
		t.Fatal(err)
	}
	refreshed, changed, err := WithMySQLBackupEgressWait(upgraded)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	wait := egressWaitOf(t, refreshed)
	if wait["image"] != testMOCOBackupImage || envOf(wait)[mysqlEgressWaitStoreEnv] != "new:9000" {
		t.Errorf("stale wait kept: %v", wait)
	}
}

func TestWithMySQLBackupEgressWait_RefusesAnUnknownJob(t *testing.T) {
	noEndpoint := MockMOCOBackupCronJob("ns", "c-mysql", testMOCOBackupImage, "")
	noContainer := MockMOCOBackupCronJob("ns", "c-mysql", testMOCOBackupImage, "http://s:9000")
	unstructured.RemoveNestedField(noContainer.Object, mocoJobPodSpecPath("containers")...)
	noCluster := MockMOCOBackupCronJob("ns", "c-mysql", testMOCOBackupImage, "http://s:9000")
	unstructured.RemoveNestedField(noCluster.Object, "spec", "jobTemplate", "spec", "template", "metadata", "labels")
	for name, cron := range map[string]*unstructured.Unstructured{"no endpoint": noEndpoint, "no container": noContainer, "no cluster": noCluster} {
		if _, _, err := WithMySQLBackupEgressWait(cron); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// answerOnLoopback answers on a loopback port until the test ends.
func answerOnLoopback(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
}

// closedPort is a loopback port nothing listens on.
func closedPort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	_ = listener.Close()
	return port
}

func runEgressWait(t *testing.T, store, database string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", "-c", mysqlEgressWaitScript)
	cmd.Env = []string{"PATH=/usr/bin:/bin", mysqlEgressWaitStoreEnv + "=" + store,
		mysqlEgressWaitDatabaseEnv + "=" + database, mysqlEgressWaitSecondsEnv + "=2"}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// The script the init container runs: it passes once the store and one
// database instance answer, and fails, naming what never answered, at its deadline.
func TestMySQLEgressWaitScript(t *testing.T) {
	if _, err := exec.LookPath("getent"); err != nil {
		t.Skip("getent is not installed")
	}
	store, database := "127.0.0.1:"+answerOnLoopback(t), "localhost:"+answerOnLoopback(t)
	if out, err := runEgressWait(t, store, database); err != nil || !strings.Contains(out, "egress answers") {
		t.Errorf("both answer: err=%v out=%q", err, out)
	}
	if out, err := runEgressWait(t, "127.0.0.1:"+closedPort(t), database); err == nil || !strings.Contains(out, "object store") {
		t.Errorf("store refuses: err=%v out=%q", err, out)
	}
	if out, err := runEgressWait(t, store, "localhost:"+closedPort(t)); err == nil || !strings.Contains(out, "database") {
		t.Errorf("database refuses: err=%v out=%q", err, out)
	}
}

// A database that starts answering within the deadline is waited for.
func TestMySQLEgressWaitScript_WaitsForALateAnswer(t *testing.T) {
	if _, err := exec.LookPath("getent"); err != nil {
		t.Skip("getent is not installed")
	}
	port := closedPort(t)
	late := make(chan net.Listener, 1)
	go func() {
		defer close(late)
		time.Sleep(700 * time.Millisecond)
		listener, err := net.Listen("tcp4", "127.0.0.1:"+port)
		if err != nil {
			return
		}
		late <- listener
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	out, err := runEgressWait(t, "127.0.0.1:"+answerOnLoopback(t), "localhost:"+port)
	if listener, ok := <-late; ok {
		_ = listener.Close()
	}
	if err != nil {
		t.Errorf("late answer: err=%v out=%q", err, out)
	}
}
