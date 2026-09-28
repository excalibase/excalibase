//go:build live

package service

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/testcontainers/testcontainers-go/modules/k3s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// Inside CloudNativePG 1.30's supported Kubernetes range.
	documentDBLiveK3s    = "rancher/k3s:v1.35.5-k3s1"
	documentDBLiveOrg    = "org1"
	documentDBLiveName   = "ddbproj"
	documentDBLiveNS     = documentDBLiveOrg + "-" + documentDBLiveName
	documentDBLiveOwner  = "owner_doc"
	documentDBLiveAppPW  = "App-Role-Password-1"
	certManagerManifest  = "https://github.com/cert-manager/cert-manager/releases/download/v1.16.2/cert-manager.yaml"
	injectorManifestPath = "testdata/documentdb-sidecar-injector.yaml"
	mongoClientImage     = "mongo:7"
	mongoProbeScript     = `const r = db.getSiblingDB("probe").items.insertOne({k: "exc-454", n: 1});
const f = db.getSiblingDB("probe").items.findOne({k: "exc-454"});
print("inserted=" + r.acknowledged + " found=" + f.k + "/" + f.n);`
	mongoFindScript  = `print("found=" + db.getSiblingDB("probe").items.findOne({k: "exc-454"}).k);`
	mongoIndexScript = `const c = db.getSiblingDB("probe").indexed;
c.insertMany(Array.from({length: 200}, (_, i) => ({i: i, k: "k" + i})));
print("count=" + c.countDocuments() + " index=" + c.createIndex({k: 1}));`
)

// labProjectAccess is the binding provisioning files in each project
// namespace; the lab binds an identity that exists in any cluster.
var labProjectAccess = k8s.ProjectAccess{ClusterRole: "cluster-admin", ServiceAccount: "default", Namespace: "default"}

type documentDBLab struct {
	ctx       context.Context
	container *k3s.K3sContainer
	// kubeconfig is set instead of container for a cluster the test did not start.
	kubeconfig string
	cs         kubernetes.Interface
	client     *k8s.Client
	// major is the Postgres major the lab project runs.
	major string
}

// Run with: go test ./internal/service/ -tags=live -run TestLiveDocumentDBOnLatestCNPG -v -count=1 -timeout 30m
func TestLiveDocumentDBOnLatestCNPG(t *testing.T) {
	lab := startDocumentDBLab(t)
	lab.installOperators(t)

	creds := lab.provision(t)
	lab.expectGatewayServingBeforeTheProvisioningStep(t)
	lab.enable(t, creds)
	lab.exposeGateway(t)
	lab.startMongoClient(t)

	out := lab.mongo(t, creds.Username, creds.Password, mongoProbeScript)
	if !strings.Contains(out, "inserted=true found=exc-454/1") {
		t.Fatalf("insert+find as the project role: %s", out)
	}
	t.Logf("project role: %s", out)
	if out := lab.mongo(t, "excalibase_app", documentDBLiveAppPW, mongoFindScript); !strings.Contains(out, "found=exc-454") {
		t.Errorf("find as the platform app role: %s", out)
	}
	if out := lab.mongo(t, creds.Username, "wrong-password", mongoFindScript); strings.Contains(out, "found=") {
		t.Errorf("a wrong password was accepted: %s", out)
	} else {
		t.Logf("wrong password: %s", out)
	}

	if out := lab.mongo(t, creds.Username, creds.Password, mongoIndexScript); !strings.Contains(out, "count=200 index=k_1") {
		t.Errorf("createIndex on a populated collection: %s", out)
	} else {
		t.Logf("index: %s", out)
	}
	lab.expectCronSucceeds(t)
	lab.expectNoGatewayRestarts(t)
	lab.expectDeletionWithinWindow(t)
}

// The cluster's initdb already created the gateway's role, so the gateway
// serves without the provisioning step having run and without restarting.
func (lab *documentDBLab) expectGatewayServingBeforeTheProvisioningStep(t *testing.T) {
	t.Helper()
	eventuallyLive(t, "gateway serving on a fresh project", 5*time.Minute, func() bool {
		ready, err := lab.client.DocumentDBGatewayReady(lab.ctx, documentDBLiveNS, documentDBLiveName+"-postgres-1")
		return err == nil && ready && lab.gatewayLogged(t, "Gateway ready to accept connections")
	})
	lab.expectNoGatewayRestarts(t)
}

func (lab *documentDBLab) expectNoGatewayRestarts(t *testing.T) {
	t.Helper()
	pod, err := lab.cs.CoreV1().Pods(documentDBLiveNS).Get(lab.ctx, documentDBLiveName+"-postgres-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read primary pod: %v", err)
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name != k8s.DocumentDBGatewayContainer {
			continue
		}
		if status.RestartCount != 0 {
			t.Fatalf("the gateway restarted %d times", status.RestartCount)
		}
		t.Logf("gateway restarts: 0")
		return
	}
	t.Fatalf("no gateway container status on %s", pod.Name)
}

// expectCronSucceeds waits for DocumentDB's scheduled jobs to run, which they only do once they can log in.
func (lab *documentDBLab) expectCronSucceeds(t *testing.T) {
	t.Helper()
	var last string
	eventuallyLive(t, "a DocumentDB cron job succeeded", 3*time.Minute, func() bool {
		out, _ := lab.client.ExecInPod(lab.ctx, documentDBLiveNS, documentDBLiveName+"-postgres-1", "postgres",
			[]string{"psql", "-U", "postgres", "-d", config.DocumentDBDatabase, "-tAc",
				"SELECT status || ':' || count(*) FROM cron.job_run_details GROUP BY status ORDER BY status"})
		last = strings.Join(strings.Fields(out), " ")
		return strings.Contains(last, "succeeded:")
	})
	t.Logf("cron runs: %s", last)
}

// expectDeletionWithinWindow tears the project down the way deletion does, inside provisioning's own budget.
func (lab *documentDBLab) expectDeletionWithinWindow(t *testing.T) {
	t.Helper()
	started := time.Now()
	if err := provisioner.NewPostgreSQLProvisioner(lab.client, "").Deprovision(lab.ctx, documentDBLiveNS, documentDBLiveName); err != nil {
		t.Fatalf("Deprovision after %s: %v", time.Since(started).Round(time.Second), err)
	}
	exists, err := lab.client.NamespaceExists(lab.ctx, documentDBLiveNS)
	if err != nil || exists {
		t.Fatalf("namespace still present after deprovision: %v", err)
	}
	t.Logf("deprovisioned in %s", time.Since(started).Round(time.Second))
}

func startDocumentDBLab(t *testing.T) *documentDBLab {
	t.Helper()
	ctx := context.Background()
	container, err := k3s.Run(ctx, documentDBLiveK3s)
	if err != nil {
		t.Fatalf("k3s start: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	kubeconfig, err := container.GetKubeConfig(ctx)
	if err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, kubeconfig, 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	// Teardown's Helm step reads its cluster from KUBECONFIG.
	t.Setenv("KUBECONFIG", path)
	client, err := k8s.NewClientWith(k8s.ClientOptions{KubeconfigPath: path})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	client = client.WithProjectAccess(labProjectAccess)
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		t.Fatalf("rest config: %v", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		t.Fatalf("clientset: %v", err)
	}
	return &documentDBLab{ctx: ctx, container: container, cs: cs, client: client}
}

// startExternalLab drives an already running cluster (a multi-node k3d) named
// by the kubeconfig in env, and skips when it is unset.
func startExternalLab(t *testing.T, env string) *documentDBLab {
	t.Helper()
	path := os.Getenv(env)
	if path == "" {
		t.Skipf("%s names no cluster", env)
	}
	t.Setenv("KUBECONFIG", path)
	client, err := k8s.NewClientWith(k8s.ClientOptions{KubeconfigPath: path})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	client = client.WithProjectAccess(labProjectAccess)
	restCfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatalf("rest config: %v", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		t.Fatalf("clientset: %v", err)
	}
	return &documentDBLab{ctx: context.Background(), kubeconfig: path, cs: cs, client: client}
}

// kubectlOutput runs kubectl inside the lab's k3s container, or against its
// external cluster.
func (lab *documentDBLab) kubectlOutput(args ...string) (string, error) {
	if lab.container == nil {
		out, err := exec.CommandContext(lab.ctx, "kubectl", append([]string{"--kubeconfig", lab.kubeconfig}, args...)...).CombinedOutput()
		return string(out), err
	}
	code, reader, err := lab.container.Exec(lab.ctx, append([]string{"kubectl"}, args...))
	out := ""
	if reader != nil {
		raw, _ := io.ReadAll(reader)
		out = string(raw)
	}
	if err == nil && code != 0 {
		err = fmt.Errorf("exit %d", code)
	}
	return out, err
}

func (lab *documentDBLab) kubectl(t *testing.T, args ...string) string {
	t.Helper()
	if lab.container == nil {
		out, err := lab.kubectlOutput(args...)
		if err != nil {
			t.Fatalf("kubectl %v: %v\n%s", args, err, out)
		}
		return out
	}
	code, reader, err := lab.container.Exec(lab.ctx, append([]string{"kubectl"}, args...))
	var out strings.Builder
	if reader != nil {
		buf := make([]byte, 4096)
		for {
			n, readErr := reader.Read(buf)
			out.Write(buf[:n])
			if readErr != nil {
				break
			}
		}
	}
	if err != nil || code != 0 {
		t.Fatalf("kubectl %v: exit %d: %v\n%s", args, code, err, out.String())
	}
	return out.String()
}

// installOperators installs CloudNativePG, cert-manager and the sidecar injector the way platform-aio does.
func (lab *documentDBLab) installOperators(t *testing.T) {
	t.Helper()
	lab.kubectl(t, "apply", "--server-side", "-f", operatorURLs[domain.PostgreSQL])
	lab.kubectl(t, "apply", "-f", certManagerManifest)
	lab.kubectl(t, "wait", "--for=condition=available", "--timeout=300s", "deployment/cnpg-controller-manager", "-n", "cnpg-system")
	lab.kubectl(t, "wait", "--for=condition=available", "--timeout=300s", "deployment", "--all", "-n", "cert-manager")
	manifest, err := os.ReadFile(injectorManifestPath)
	if err != nil {
		t.Fatalf("read injector manifest: %v", err)
	}
	if err := lab.container.CopyToContainer(lab.ctx, manifest, "/tmp/injector.yaml", 0o644); err != nil {
		t.Fatalf("copy injector manifest: %v", err)
	}
	eventuallyLive(t, "injector applied once the cert-manager webhook answers", 3*time.Minute, func() bool {
		code, _, err := lab.container.Exec(lab.ctx, []string{"kubectl", "apply", "-f", "/tmp/injector.yaml"})
		return err == nil && code == 0
	})
	lab.kubectl(t, "wait", "--for=condition=available", "--timeout=300s", "deployment/documentdb-sidecar-injector", "-n", "cnpg-system")
	// The operator lists plugins once at start-up (see the chart's note).
	lab.kubectl(t, "rollout", "restart", "deployment/cnpg-controller-manager", "-n", "cnpg-system")
	lab.kubectl(t, "rollout", "status", "deployment/cnpg-controller-manager", "-n", "cnpg-system", "--timeout=300s")
}

// provision runs the platform's own provisioner, so the cluster is exactly what a DocumentDB project gets.
func (lab *documentDBLab) provision(t *testing.T) *provisioner.ProvisioningResult {
	t.Helper()
	return lab.provisionMajor(t, "17")
}

func (lab *documentDBLab) provisionMajor(t *testing.T, major string) *provisioner.ProvisioningResult {
	t.Helper()
	req := domain.ProvisioningRequest{
		ProjectName: documentDBLiveName, OrgID: documentDBLiveOrg, DBType: domain.PostgreSQL,
		PostgresVersion: major, DatabaseName: "appdb", MasterUsername: documentDBLiveOwner, DocumentDB: true,
	}
	tier := config.TierConfig{Instances: 1, StorageSize: "1Gi", Memory: "1Gi", CPU: "0.5"}
	creds, err := provisioner.NewPostgreSQLProvisioner(lab.client, "").
		ProvisionWithRollback(lab.ctx, req, tier, provisioner.NewProvisionContext(nil, nil))
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	pod, err := lab.cs.CoreV1().Pods(documentDBLiveNS).Get(lab.ctx, documentDBLiveName+"-postgres-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read primary pod: %v", err)
	}
	if !podHasContainer(pod, k8s.DocumentDBGatewayContainer) {
		t.Fatalf("no gateway container was injected into %s", pod.Name)
	}
	return creds
}

func podHasContainer(pod *corev1.Pod, name string) bool {
	for _, container := range pod.Spec.Containers {
		if container.Name == name {
			return true
		}
	}
	return false
}

// enable runs the service's own DocumentDB step, after the app role project registration creates first.
func (lab *documentDBLab) enable(t *testing.T, creds *provisioner.ProvisioningResult) {
	t.Helper()
	lab.enableMajor(t, creds, "17")
}

func (lab *documentDBLab) enableMajor(t *testing.T, creds *provisioner.ProvisioningResult, major string) {
	t.Helper()
	_, err := lab.client.ExecInPod(lab.ctx, documentDBLiveNS, documentDBLiveName+"-postgres-1", "postgres",
		[]string{"psql", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-c",
			"CREATE ROLE excalibase_app LOGIN PASSWORD '" + documentDBLiveAppPW + "'"})
	if err != nil {
		t.Fatalf("create app role: %v", err)
	}
	inst := &domain.DatabaseInstance{
		ProjectID: documentDBLiveName, Namespace: documentDBLiveNS, DatabaseName: creds.DatabaseName,
		Username: creds.Username, PostgresVersion: major, DocumentDB: true, DeploymentMode: domain.ModeK8s,
	}
	svc := NewProvisioningService(documentDBStore(t), provisioner.NewFactory(), lab.client)
	if err := svc.enableDocumentDB(lab.ctx, inst, idleContext()); err != nil {
		t.Fatalf("enableDocumentDB: %v", err)
	}
	eventuallyLive(t, "gateway accepting connections", 8*time.Minute, func() bool {
		ready, err := lab.client.DocumentDBGatewayReady(lab.ctx, documentDBLiveNS, documentDBLiveName+"-postgres-1")
		return err == nil && ready && lab.gatewayLogged(t, "Gateway ready to accept connections")
	})
}

func (lab *documentDBLab) gatewayLogged(t *testing.T, line string) bool {
	t.Helper()
	raw, err := lab.cs.CoreV1().Pods(documentDBLiveNS).GetLogs(documentDBLiveName+"-postgres-1",
		&corev1.PodLogOptions{Container: k8s.DocumentDBGatewayContainer}).DoRaw(lab.ctx)
	return err == nil && strings.Contains(string(raw), line)
}

func (lab *documentDBLab) exposeGateway(t *testing.T) {
	t.Helper()
	if err := lab.client.EnsureDocumentDBService(lab.ctx, documentDBLiveNS, documentDBLiveName); err != nil {
		t.Fatalf("EnsureDocumentDBService: %v", err)
	}
}

// startMongoClient mounts the cluster CA so the client verifies the gateway's chain.
func (lab *documentDBLab) startMongoClient(t *testing.T) {
	t.Helper()
	noGrace := int64(0)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "mongo", Namespace: documentDBLiveNS},
		Spec: corev1.PodSpec{
			RestartPolicy:                 corev1.RestartPolicyNever,
			TerminationGracePeriodSeconds: &noGrace,
			Containers: []corev1.Container{{
				Name: "mongo", Image: mongoClientImage, Command: []string{"sleep", "3600"},
				VolumeMounts: []corev1.VolumeMount{{Name: "ca", MountPath: "/ca"}},
			}},
			Volumes: []corev1.Volume{{Name: "ca", VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: documentDBLiveName + "-postgres-ca"},
			}}},
		},
	}
	if _, err := lab.cs.CoreV1().Pods(documentDBLiveNS).Create(lab.ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create mongo client: %v", err)
	}
	eventuallyLive(t, "mongo client ready", 5*time.Minute, func() bool {
		ready, _ := lab.client.IsPodReady(lab.ctx, documentDBLiveNS, "mongo")
		return ready
	})
}

// mongo dials the gateway Service by name, verifying its certificate and hostname, with SCRAM-SHA-256.
func (lab *documentDBLab) mongo(t *testing.T, user, password, script string) string {
	t.Helper()
	host := documentDBLiveName + "-documentdb." + documentDBLiveNS + ".svc.cluster.local"
	uri := "mongodb://" + user + ":" + password + "@" + host + ":10260/?tls=true&tlsCAFile=/ca/ca.crt" +
		"&authMechanism=SCRAM-SHA-256"
	out, err := lab.client.ExecInPod(lab.ctx, documentDBLiveNS, "mongo", "mongo",
		[]string{"mongosh", "--quiet", uri, "--eval", script})
	if err != nil {
		return strings.TrimSpace(out) + " " + err.Error()
	}
	return strings.TrimSpace(out)
}

func eventuallyLive(t *testing.T, desc string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not true after %s", desc, timeout)
		}
		time.Sleep(5 * time.Second)
	}
}
