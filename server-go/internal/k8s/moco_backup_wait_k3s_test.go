//go:build live

package k8s

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/k3s"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/excalibase/provisioning-poc/internal/config"
)

// EXC-586: k3s's own policy controller (kube-router) admits a new pod to its
// peers' NetworkPolicies a moment after the pod starts; MOCO's backup dials
// at once and failed both its attempts. This runs MOCO's real backup Job
// under the platform's real project policies, waited and not.

const (
	mocoLabNamespace = "org1-mocoproj"
	mocoLabProject   = "mb"
	mocoLabStoreNS   = "platform"
	mocoLabBucket    = "backups"
	mocoLabKey       = "labadmin"
	mocoLabSecret    = "labadmin-secret-123"
	// The versions rke2/versions.env installs.
	mocoLabCertManager = "https://github.com/cert-manager/cert-manager/releases/download/v1.16.2/cert-manager.yaml"
	mocoLabMOCO        = "https://github.com/cybozu-go/moco/releases/download/v0.37.0/moco.yaml"
	// The images the platform-aio chart and the MySQL catalogue pin.
	mocoLabRustFS      = "docker.io/rustfs/rustfs:1.0.1@sha256:1803faef57627e2d9c2e7d89d655d712ddded5389040054987163043fecb6a3c"
	mocoLabRustFSCli   = "docker.io/rustfs/rc:v0.1.36@sha256:ab024bfebee49a750ce886b4c70963ccd9ddaa03f491704a90710641d7a26699"
	mocoLabMySQLImage  = "ghcr.io/cybozu-go/moco/mysql:8.4.10@sha256:6a8b7362a5b15dd5343c69f316adfc5d1798924470b1d38ce9cad85febb23120"
	mocoLabWaitedRuns  = 5
	mocoLabControlRuns = 3
)

type mocoLab struct {
	*egressLab
	container *k3s.K3sContainer
}

// Run with: go test ./internal/k8s/ -tags=live -run TestK3sMySQLBackupWaitsForEgress -v -count=1 -timeout 30m
func TestK3sMySQLBackupWaitsForEgress(t *testing.T) {
	lab := startMOCOLab(t)
	lab.startStore(t)
	lab.startMySQL(t)
	cron := lab.backupCronJob(t)

	failed := 0
	for run := 0; run < mocoLabControlRuns; run++ {
		if lab.runBackup(t, unwaitedBackupJob(t, cron, run)) != 0 {
			failed++
		}
	}
	t.Logf("without the wait, %d of %d backups failed (kube-router's window is timing-dependent)", failed, mocoLabControlRuns)

	waited, changed, err := WithMySQLBackupEgressWait(cron)
	if err != nil || !changed {
		t.Fatalf("WithMySQLBackupEgressWait: changed=%v err=%v", changed, err)
	}
	if err := lab.client.UpdateCRD(lab.ctx, CronJobGVR, mocoLabNamespace, waited); err != nil {
		t.Fatalf("give the CronJob the wait: %v", err)
	}
	cron = lab.backupCronJob(t)
	if _, changed, err := WithMySQLBackupEgressWait(cron); err != nil || changed {
		t.Fatalf("the CronJob as the API server keeps it does not read as waiting: changed=%v err=%v", changed, err)
	}
	for run := 0; run < mocoLabWaitedRuns; run++ {
		job, err := BuildManualMySQLBackupJob(cron, time.Now().Add(time.Duration(run)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if failedPods := lab.runBackup(t, job); failedPods != 0 {
			t.Errorf("waited backup %s: %d pod(s) failed; every run must succeed on its first pod", job.GetName(), failedPods)
		}
	}
}

// Run with: go test ./internal/k8s/ -tags=live -run TestK3sCiliumMySQLBackupNeedsNoWait -v -count=1 -timeout 30m
// Production (RKE2) runs Cilium, which enforces a pod's policy before its
// network comes up: MOCO's backup as MOCO renders it must pass every time.
func TestK3sCiliumMySQLBackupNeedsNoWait(t *testing.T) {
	egress := &egressLab{ctx: context.Background(), ciliumValues: productionCiliumValues, ciliumChart: productionCiliumChartVersion}
	container := egress.startCiliumClusterWith(t, testcontainers.WithCmdArgs("--disable-kube-proxy", "--disable=traefik", "--disable=servicelb"))
	lab := &mocoLab{egressLab: egress, container: container}
	lab.installMOCO(t)
	lab.startStore(t)
	lab.startMySQL(t)
	cron := lab.backupCronJob(t)
	for run := 0; run < mocoLabWaitedRuns; run++ {
		if failed := lab.runBackup(t, unwaitedBackupJob(t, cron, run)); failed != 0 {
			t.Errorf("unwaited backup %d on Cilium: %d pod(s) failed", run, failed)
		}
	}
}

func startMOCOLab(t *testing.T) *mocoLab {
	t.Helper()
	ctx := context.Background()
	container, err := k3s.Run(ctx, "rancher/k3s:v1.31.6-k3s1")
	if err != nil {
		t.Fatalf("k3s start: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	kubeconfig, err := container.GetKubeConfig(ctx)
	if err != nil {
		t.Fatalf("get kubeconfig: %v", err)
	}
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		t.Fatalf("parse kubeconfig: %v", err)
	}
	lab := &mocoLab{egressLab: &egressLab{ctx: ctx}, container: container}
	lab.useCluster(t, restCfg)
	lab.installMOCO(t)
	return lab
}

func (lab *mocoLab) installMOCO(t *testing.T) {
	t.Helper()
	lab.kubectlApply(t, mocoLabCertManager)
	lab.waitDeployments(t, "cert-manager", "cert-manager", "cert-manager-webhook", "cert-manager-cainjector")
	eventually(t, "MOCO installed", 3*time.Minute, func() bool { return lab.tryKubectlApply(mocoLabMOCO) == nil })
	lab.waitDeployments(t, "moco-system", "moco-controller")
}

func (lab *mocoLab) tryKubectlApply(manifest string) error {
	code, out, err := lab.container.Exec(lab.ctx, []string{"kubectl", "apply", "--server-side", "--force-conflicts", "-f", manifest})
	if err == nil && code == 0 {
		return nil
	}
	body, _ := io.ReadAll(out)
	return fmt.Errorf("kubectl apply %s: exit %d: %v: %s", manifest, code, err, body)
}

func (lab *mocoLab) kubectlApply(t *testing.T, manifest string) {
	t.Helper()
	if err := lab.tryKubectlApply(manifest); err != nil {
		t.Fatal(err)
	}
}

func (lab *mocoLab) waitDeployments(t *testing.T, namespace string, names ...string) {
	t.Helper()
	for _, name := range names {
		eventually(t, namespace+"/"+name+" available", 5*time.Minute, func() bool {
			deployment, err := lab.cs.AppsV1().Deployments(namespace).Get(lab.ctx, name, metav1.GetOptions{})
			return err == nil && deploymentAvailable(deployment)
		})
	}
}

// startStore runs the bundled object store behind the chart's rustfs policy
// (platform-aio objectstore.yaml with mysql.enabled) and makes the bucket.
func (lab *mocoLab) startStore(t *testing.T) {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: mocoLabStoreNS}}
	if _, err := lab.cs.CoreV1().Namespaces().Create(lab.ctx, ns, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create %s: %v", mocoLabStoreNS, err)
	}
	store := map[string]string{"app": "rustfs"}
	env := []corev1.EnvVar{
		{Name: "RUSTFS_VOLUMES", Value: "/data"}, {Name: "RUSTFS_ADDRESS", Value: ":9000"},
		{Name: "RUSTFS_CONSOLE_ENABLE", Value: "false"}, {Name: "RUSTFS_OBS_LOG_DIRECTORY", Value: "/logs"},
		{Name: "RUSTFS_ACCESS_KEY", Value: mocoLabKey}, {Name: "RUSTFS_SECRET_KEY", Value: mocoLabSecret},
	}
	scratch := func(name string) corev1.Volume {
		return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "rustfs", Namespace: mocoLabStoreNS, Labels: store},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "rustfs", Image: mocoLabRustFS, Env: env, Ports: []corev1.ContainerPort{{ContainerPort: 9000}},
				VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}, {Name: "logs", MountPath: "/logs"}},
			}},
			Volumes: []corev1.Volume{scratch("data"), scratch("logs")},
		},
	}
	if _, err := lab.cs.CoreV1().Pods(mocoLabStoreNS).Create(lab.ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create the store: %v", err)
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "rustfs", Namespace: mocoLabStoreNS},
		Spec:       corev1.ServiceSpec{Selector: store, Ports: []corev1.ServicePort{{Port: 9000, TargetPort: intstr.FromInt(9000)}}},
	}
	if _, err := lab.cs.CoreV1().Services(mocoLabStoreNS).Create(lab.ctx, svc, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create the store's Service: %v", err)
	}
	lab.applyStorePolicy(t, store)
	setup := "until rc alias set store http://rustfs:9000 " + mocoLabKey + " " + mocoLabSecret +
		" && rc ls store/; do sleep 2; done; rc mb store/" + mocoLabBucket
	lab.runOnce(t, mocoLabStoreNS, "objectstore-setup", mocoLabRustFSCli, []string{"sh", "-c", setup})
	eventually(t, "bucket made", 4*time.Minute, func() bool {
		got, err := lab.cs.CoreV1().Pods(mocoLabStoreNS).Get(lab.ctx, "objectstore-setup", metav1.GetOptions{})
		return err == nil && got.Status.Phase == corev1.PodSucceeded
	})
}

func (lab *mocoLab) applyStorePolicy(t *testing.T, store map[string]string) {
	t.Helper()
	port := intstr.FromInt(9000)
	policy := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "rustfs", Namespace: mocoLabStoreNS},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: store},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				Ports: []networkingv1.NetworkPolicyPort{{Port: &port}},
				From: []networkingv1.NetworkPolicyPeer{
					{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "objectstore-setup"}}},
					{
						NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"excalibase.io/type": "project"}},
						PodSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"app.kubernetes.io/created-by": "moco"},
							MatchExpressions: []metav1.LabelSelectorRequirement{{
								Key: "app.kubernetes.io/name", Operator: metav1.LabelSelectorOpIn, Values: []string{"mysql-backup", "mysql-restore"},
							}},
						},
					},
				},
			}},
		},
	}
	if _, err := lab.cs.NetworkingV1().NetworkPolicies(mocoLabStoreNS).Create(lab.ctx, policy, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create the store's policy: %v", err)
	}
}

// startMySQL makes the project namespace with its real isolation policies and
// a one-instance cluster backed up by the project's BackupPolicy.
func (lab *mocoLab) startMySQL(t *testing.T) {
	t.Helper()
	lab.createProjectNamespaces(t, map[string]string{mocoLabNamespace: "org1"})
	tier := config.TierConfig{Instances: 1, StorageSize: "1Gi", MaxStorageSize: "2Gi", Memory: "512Mi", CPU: "250m"}
	opts := MySQLClusterOpts{ProjectID: mocoLabProject, Namespace: mocoLabNamespace, Tier: tier, Image: mocoLabMySQLImage}
	creds := map[string][]byte{"AWS_ACCESS_KEY_ID": []byte(mocoLabKey), "AWS_SECRET_ACCESS_KEY": []byte(mocoLabSecret), "AWS_REGION": []byte("us-east-1")}
	if err := lab.client.CreateSecret(lab.ctx, mocoLabNamespace, MySQLBackupAccessName, creds); err != nil {
		t.Fatalf("backup credentials: %v", err)
	}
	policy, err := BuildMySQLBackupPolicy(MySQLBackupOpts{ProjectID: mocoLabProject, Namespace: mocoLabNamespace,
		Schedule: "0 0 1 1 *", Plan: tier, Disk: "1Gi",
		Store: MySQLBackupStore{Bucket: mocoLabBucket, Endpoint: "http://rustfs." + mocoLabStoreNS + ".svc:9000", Region: "us-east-1", PathStyle: true}})
	if err != nil {
		t.Fatal(err)
	}
	settings, err := BuildMySQLConfig(opts)
	if err != nil {
		t.Fatal(err)
	}
	cluster, err := BuildMySQLCluster(opts)
	if err != nil {
		t.Fatal(err)
	}
	lab.apply(t, ServiceAccountGVR, BuildMySQLBackupServiceAccount(mocoLabNamespace))
	lab.apply(t, ConfigMapGVR, settings)
	lab.apply(t, MOCOBackupPolicyGVR, policy)
	lab.apply(t, MOCOClusterGVR, WithMySQLBackupPolicy(cluster, mocoLabProject))
	eventually(t, "MySQL available", 10*time.Minute, func() bool {
		got, err := lab.client.GetCRD(lab.ctx, MOCOClusterGVR, mocoLabNamespace, MySQLClusterName(mocoLabProject))
		if err != nil {
			return false
		}
		conditions, _, _ := unstructured.NestedSlice(got.Object, "status", "conditions")
		for _, raw := range conditions {
			condition, _ := raw.(map[string]interface{})
			if condition["type"] == "Available" && condition["status"] == "True" {
				return true
			}
		}
		return false
	})
}

// apply retries while MOCO's webhook is still coming up.
func (lab *mocoLab) apply(t *testing.T, gvr schema.GroupVersionResource, object *unstructured.Unstructured) {
	t.Helper()
	eventually(t, "apply "+object.GetName(), 2*time.Minute, func() bool {
		return lab.client.ApplyCRD(lab.ctx, gvr, mocoLabNamespace, object) == nil
	})
}

func (lab *mocoLab) backupCronJob(t *testing.T) *unstructured.Unstructured {
	t.Helper()
	var cron *unstructured.Unstructured
	eventually(t, "MOCO's backup CronJob", 2*time.Minute, func() bool {
		got, err := lab.client.GetCRD(lab.ctx, CronJobGVR, mocoLabNamespace, MySQLBackupCronJobName(mocoLabProject))
		cron = got
		return err == nil
	})
	return cron
}

// unwaitedBackupJob is a run of MOCO's CronJob as MOCO renders it.
func unwaitedBackupJob(t *testing.T, cron *unstructured.Unstructured, run int) *unstructured.Unstructured {
	t.Helper()
	spec, _, _ := unstructured.NestedMap(cron.Object, "spec", "jobTemplate", "spec")
	job := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "batch/v1", "kind": "Job", "spec": spec}}
	job.SetName(fmt.Sprintf("unwaited-%d", run))
	job.SetNamespace(cron.GetNamespace())
	return job
}

// runBackup starts job, waits for it to finish and returns how many of its
// pods failed, counting a job that never succeeded as one more.
func (lab *mocoLab) runBackup(t *testing.T, job *unstructured.Unstructured) int64 {
	t.Helper()
	if err := lab.client.ApplyCRD(lab.ctx, JobGVR, mocoLabNamespace, job); err != nil {
		t.Fatalf("start %s: %v", job.GetName(), err)
	}
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		got, err := lab.client.GetCRD(lab.ctx, JobGVR, mocoLabNamespace, job.GetName())
		if err == nil {
			state := MySQLBackupJobState(got)
			failed, _, _ := unstructured.NestedInt64(got.Object, "status", "failed")
			if state.Phase != MySQLJobRunning {
				t.Logf("%s: %s, %d failed pod(s)%s", job.GetName(), state.Phase, failed, lab.podOutput(job.GetName()))
				lab.deleteRun(t, job.GetName())
				if state.Phase != MySQLJobSucceeded {
					return failed + 1
				}
				return failed
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("%s did not finish in 5m:%s%s", job.GetName(), lab.podOutput(job.GetName()), lab.events())
	return 0
}

func (lab *mocoLab) podOutput(job string) string {
	pods, err := lab.cs.CoreV1().Pods(mocoLabNamespace).List(lab.ctx, metav1.ListOptions{LabelSelector: "job-name=" + job})
	if err != nil {
		return ""
	}
	var out strings.Builder
	for _, pod := range pods.Items {
		fmt.Fprintf(&out, "\n  %s: %s", pod.Name, pod.Status.Phase)
		for _, condition := range pod.Status.Conditions {
			fmt.Fprintf(&out, "\n    %s=%s %s", condition.Type, condition.Status, condition.Message)
		}
		for _, status := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
			fmt.Fprintf(&out, "\n    %s: %+v", status.Name, status.State)
		}
		for _, container := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
			tail := int64(2)
			raw, err := lab.cs.CoreV1().Pods(mocoLabNamespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: container.Name, TailLines: &tail}).DoRaw(lab.ctx)
			if err == nil {
				fmt.Fprintf(&out, "\n  %s/%s: %s", pod.Name, container.Name, strings.TrimSpace(string(raw)))
			}
		}
	}
	return out.String()
}

// deleteRun removes a finished run and its pods' work volumes, which count
// against the project's quota; MOCO's history limits do this for real runs.
func (lab *mocoLab) deleteRun(t *testing.T, job string) {
	t.Helper()
	foreground := metav1.DeletePropagationForeground
	if err := lab.cs.BatchV1().Jobs(mocoLabNamespace).Delete(lab.ctx, job, metav1.DeleteOptions{PropagationPolicy: &foreground}); err != nil {
		t.Fatalf("delete %s: %v", job, err)
	}
	eventually(t, job+"'s work volumes released", 2*time.Minute, func() bool {
		claims, err := lab.cs.CoreV1().PersistentVolumeClaims(mocoLabNamespace).List(lab.ctx, metav1.ListOptions{})
		if err != nil {
			return false
		}
		for _, claim := range claims.Items {
			if strings.HasPrefix(claim.Name, job+"-") {
				return false
			}
		}
		return true
	})
}

func (lab *mocoLab) events() string {
	events, err := lab.cs.CoreV1().Events(mocoLabNamespace).List(lab.ctx, metav1.ListOptions{})
	if err != nil {
		return ""
	}
	var out strings.Builder
	for _, event := range events.Items {
		if event.Type == corev1.EventTypeWarning {
			fmt.Fprintf(&out, "\n  event %s %s: %s", event.InvolvedObject.Name, event.Reason, event.Message)
		}
	}
	return out.String()
}

// runOnce starts a pod, labelled app=<name>, that runs command once in image.
func (lab *mocoLab) runOnce(t *testing.T, namespace, name, image string, command []string) {
	t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: map[string]string{"app": name}},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers:    []corev1.Container{{Name: name, Image: image, Command: command}},
		},
	}
	if _, err := lab.cs.CoreV1().Pods(namespace).Create(lab.ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create pod %s/%s: %v", namespace, name, err)
	}
}
