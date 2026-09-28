//go:build live

package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Clusters these tests drive; each skips when its variable is unset.
const (
	multiNodeKubeconfigEnv  = "EXCALIBASE_LIVE_KUBECONFIG"
	singleNodeKubeconfigEnv = "EXCALIBASE_LIVE_SINGLE_NODE_KUBECONFIG"
	spreadLiveSource        = "hasrc"
	spreadLiveRestored      = "harst"
)

// liveService is the provisioning service over a real cluster, with the org on STANDARD.
func liveService(t *testing.T, client k8s.KubeClient, store *domain.S3Credentials) *ProvisioningService {
	t.Helper()
	instances, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	svc := NewProvisioningService(instances, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(client, "")), client)
	orgs := fakestore.NewOrgs()
	orgs.AddOrg(backupLiveOrg, domain.Standard)
	svc.SetOrgStore(orgs)
	svc.SetVault(newFakeVault())
	defaults := &BackupDefaults{AccessKeyID: "k", SecretAccessKey: "s", Endpoint: testR2Endpoint, Bucket: "backups", Schedule: backupLiveSchedule, RetentionDays: 7}
	if store != nil {
		defaults = &BackupDefaults{AccessKeyID: store.AccessKeyID, SecretAccessKey: store.SecretAccessKey, Endpoint: store.Endpoint,
			Bucket: store.Bucket, Region: store.Region, Schedule: backupLiveSchedule, RetentionDays: 7}
	}
	if err := svc.SetBackupDefaults(defaults); err != nil {
		t.Fatalf("SetBackupDefaults: %v", err)
	}
	return svc
}

// Run with: EXCALIBASE_LIVE_SINGLE_NODE_KUBECONFIG=<1-node k3d> go test ./internal/service/ -tags=live -run TestLiveStandardIsRefusedOnASingleNode -v -count=1
func TestLiveStandardIsRefusedOnASingleNode(t *testing.T) {
	lab := startExternalLab(t, singleNodeKubeconfigEnv)
	svc := liveService(t, lab.client, nil)
	before := namespaceCount(t, lab)

	_, err := svc.Provision(lab.ctx, domain.ProvisioningRequest{
		ProjectName: "one-node", OrgID: backupLiveOrg, DBType: domain.PostgreSQL, PostgresVersion: "17",
	})
	var tooFew *NotEnoughNodesError
	if !errors.As(err, &tooFew) || tooFew.Instances != 3 || tooFew.Nodes != 1 {
		t.Fatalf("a STANDARD project on one node must be refused for its nodes, got %v", err)
	}
	t.Logf("refused: %v", err)
	if after := namespaceCount(t, lab); after != before {
		t.Errorf("the refusal created something: %d namespaces before, %d after", before, after)
	}
}

// freshNamespaces removes what an earlier run on the same external cluster
// left, so this run's store credentials and projects start clean.
func (lab *documentDBLab) freshNamespaces(t *testing.T, names ...string) {
	t.Helper()
	lab.kubectl(t, append([]string{"delete", "namespace", "--ignore-not-found", "--wait=true", "--timeout=900s"}, names...)...)
}

func namespaceCount(t *testing.T, lab *documentDBLab) int {
	t.Helper()
	list, err := lab.cs.CoreV1().Namespaces().List(lab.ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list namespaces: %v", err)
	}
	return len(list.Items)
}

// Run with: EXCALIBASE_LIVE_KUBECONFIG=<k3d with 3 agents> go test ./internal/service/ -tags=live -run TestLiveStandardSpreadsAndRestoresAtItsTier -v -count=1 -timeout 60m
func TestLiveStandardSpreadsAndRestoresAtItsTier(t *testing.T) {
	lab := &backupLab{documentDBLab: startExternalLab(t, multiNodeKubeconfigEnv)}
	lab.freshNamespaces(t, "backup-store", backupLiveOrg+"-"+spreadLiveSource, backupLiveOrg+"-"+spreadLiveRestored)
	lab.installBackupStack(t)
	svc := liveService(t, lab.client, lab.store)
	tier, err := svc.TierConfig(lab.ctx, domain.Standard)
	if err != nil {
		t.Fatalf("tier: %v", err)
	}
	if err := svc.RequireNodeSpread(lab.ctx, domain.Standard, tier); err != nil {
		t.Fatalf("admission on this cluster: %v", err)
	}

	lab.provisionAt(t, spreadLiveSource, tier)
	lab.expectSpreadAtTier(t, spreadLiveSource, tier)
	lab.write(t, spreadLiveSource, 1, 1000)
	atBackup := lab.checksums(t, spreadLiveSource)
	backupID := lab.takeBackup(t)

	lab.restoreWithPlans(t, spreadLiveRestored, svc, domain.RestoreRequest{BackupID: backupID})
	lab.expectSpreadAtTier(t, spreadLiveRestored, tier)
	if got := lab.checksums(t, spreadLiveRestored); got != atBackup {
		t.Fatalf("restored rows %v, want %v", got, atBackup)
	}
}

// provisionAt runs the platform's provisioner for a project at the tier, backups on.
func (lab *backupLab) provisionAt(t *testing.T, project string, tier config.TierConfig) {
	t.Helper()
	req := domain.ProvisioningRequest{
		ProjectName: project, OrgID: backupLiveOrg, DBType: domain.PostgreSQL, PostgresVersion: "17",
		Backup: &domain.BackupSettings{Enabled: true, Schedule: backupLiveSchedule, Retention: 7, S3: lab.store},
	}
	started := time.Now()
	creds, err := provisioner.NewPostgreSQLProvisioner(lab.client, "").
		ProvisionWithRollback(lab.ctx, req, tier, provisioner.NewProvisionContext(nil, nil))
	if err != nil {
		t.Fatalf("provision %s: %v", project, err)
	}
	t.Logf("%s provisioned in %s", project, time.Since(started).Round(time.Second))
	enabled := true
	lab.source = &domain.DatabaseInstance{
		ProjectID: project, OrgID: backupLiveOrg, Namespace: backupLiveOrg + "-" + project, Tier: domain.Standard,
		DBType: domain.PostgreSQL, PostgresVersion: "17", DeploymentMode: domain.ModeK8s, Status: "ACTIVE",
		DatabaseName: creds.DatabaseName, Username: creds.Username, Password: creds.Password, BackupEnabled: &enabled,
	}
	lab.adapter = NewK8sBackupAdapter(lab.client, StaticBackupStorage(lab.store))
	lab.psql(t, project, "CREATE TABLE customers (id int PRIMARY KEY, name text NOT NULL);"+
		"CREATE TABLE orders (id int PRIMARY KEY, customer_id int REFERENCES customers(id), amount numeric(10,2));")
}

// restoreWithPlans restores through the adapter with the service deciding the
// restored project's plan, exactly as production wires it.
func (lab *backupLab) restoreWithPlans(t *testing.T, project string, plans RestorePlanSource, req domain.RestoreRequest) {
	t.Helper()
	store := emptyInstanceStore(t)
	lab.adapter.SetInstanceStore(store)
	lab.adapter.SetProjectRegistrar(&fakeRegistrar{store: store})
	lab.adapter.SetDatabaseProbe(psqlProbe{lab: lab})
	lab.adapter.SetRestorePlanSource(plans)
	req.NewProjectName, req.TargetProjectID = project, project
	started := time.Now()
	if _, err := lab.adapter.Restore(lab.ctx, lab.source, req); err != nil {
		t.Fatalf("restore %s: %v", project, err)
	}
	t.Logf("restore %s usable in %s", project, time.Since(started).Round(time.Second))
}

// expectSpreadAtTier checks the live Cluster carries the tier's size and
// limits, and that all its instances run, each on its own node.
func (lab *backupLab) expectSpreadAtTier(t *testing.T, project string, tier config.TierConfig) {
	t.Helper()
	namespace := backupLiveOrg + "-" + project
	cluster, err := lab.client.GetCRD(lab.ctx, k8s.CNPGClusterGVR, namespace, project+"-postgres")
	if err != nil {
		t.Fatalf("read cluster: %v", err)
	}
	instances, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "instances")
	size, _, _ := unstructured.NestedString(cluster.Object, "spec", "storage", "size")
	cpu, _, _ := unstructured.NestedString(cluster.Object, "spec", "resources", "limits", "cpu")
	memory, _, _ := unstructured.NestedString(cluster.Object, "spec", "resources", "limits", "memory")
	antiAffinity, _, _ := unstructured.NestedString(cluster.Object, "spec", "affinity", "podAntiAffinityType")
	if int(instances) != tier.Instances || size != tier.StorageSize || cpu != tier.CPU || memory != tier.Memory || antiAffinity != "required" {
		t.Fatalf("%s: instances=%d size=%s limits=%s/%s antiAffinity=%q, want the tier %+v", project, instances, size, cpu, memory, antiAffinity, tier)
	}
	nodes := map[string]string{}
	eventuallyLive(t, project+" runs every instance ready", 10*time.Minute, func() bool {
		pods, err := lab.cs.CoreV1().Pods(namespace).List(lab.ctx, metav1.ListOptions{LabelSelector: "cnpg.io/cluster=" + project + "-postgres,cnpg.io/podRole=instance"})
		if err != nil {
			return false
		}
		clear(nodes)
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning && podReady(pod.Status.Conditions) {
				nodes[pod.Name] = pod.Spec.NodeName
			}
		}
		return len(nodes) == tier.Instances
	})
	distinct := map[string]bool{}
	for _, node := range nodes {
		distinct[node] = true
	}
	if len(distinct) != tier.Instances {
		t.Fatalf("%s: instances share nodes: %v", project, nodes)
	}
	t.Logf("%s: %d instances, %s, limits %s CPU / %s, on nodes %v", project, instances, size, cpu, memory, nodes)
	t.Logf("%s pods:\n%s", project, strings.TrimSpace(lab.kubectl(t, "get", "pods", "-n", namespace, "-o", "wide", "-l", "cnpg.io/podRole=instance")))
}

func podReady(conditions []corev1.PodCondition) bool {
	for _, condition := range conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
