//go:build integration

package service

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// EXC-587 on a real MOCO cluster: a backup, writes, a ship of the backup
// source's binlogs, more writes; then MOCO restores the backup into a new
// namespace and the shipped binlogs are replayed to a second between writes,
// and to the last ship. The source must stay healthy: the flush adds no
// errant transaction on a replica.
//
// Needs a cluster with cert-manager and MOCO, and a bucket both the cluster
// and this process reach:
//
//	MOCO_IT_KUBECONFIG=... MOCO_IT_S3_CLUSTER=http://s3:9000 MOCO_IT_S3_HOST=http://localhost:19587 \
//	MOCO_IT_S3_KEY=... MOCO_IT_S3_SECRET=... MOCO_IT_BUCKET=... [MOCO_IT_INSTANCES=3] \
//	go test -tags=integration ./internal/service/ -run TestMOCOShippedBinlogs -v -timeout 40m
func TestMOCOShippedBinlogsReachPastTheLastBackup(t *testing.T) {
	kubeconfig := os.Getenv("MOCO_IT_KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("MOCO_IT_KUBECONFIG is not set")
	}
	instances, _ := strconv.Atoi(os.Getenv("MOCO_IT_INSTANCES"))
	if instances == 0 {
		instances = 3
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)
	defer cancel()
	raw, err := k8s.NewClientWith(k8s.ClientOptions{KubeconfigPath: kubeconfig})
	if err != nil {
		t.Fatal(err)
	}
	// The throwaway cluster's default account stands in for provisioning's.
	client := raw.WithProjectAccess(k8s.ProjectAccess{ClusterRole: "cluster-admin", ServiceAccount: "default", Namespace: "default"})
	lab := newMOCOLab(t, client, instances)

	lab.sql(ctx, t, lab.namespace, lab.project, "CREATE TABLE app.notes (id INT AUTO_INCREMENT PRIMARY KEY, body TEXT)", insertMOCONotes(100))
	lab.backup(ctx, t)
	lab.sql(ctx, t, lab.namespace, lab.project, insertMOCONotes(50))
	time.Sleep(1100 * time.Millisecond)
	point := time.Now().UTC().Truncate(time.Second)
	time.Sleep(1100 * time.Millisecond)
	lab.sql(ctx, t, lab.namespace, lab.project, insertMOCONotes(25))
	time.Sleep(1100 * time.Millisecond)
	if err := lab.adapter.ShipBinlogs(ctx, lab.inst); err != nil {
		t.Fatalf("ShipBinlogs: %v", err)
	}
	lab.sql(ctx, t, lab.namespace, lab.project, insertMOCONotes(10))
	time.Sleep(1100 * time.Millisecond)
	if err := lab.adapter.ShipBinlogs(ctx, lab.inst); err != nil {
		t.Fatalf("second ShipBinlogs: %v", err)
	}
	lab.requireHealthy(ctx, t)

	window, err := lab.adapter.RestoreWindow(ctx, lab.inst)
	if err != nil || window == nil || window.Latest.Before(point) {
		t.Fatalf("window %+v %v does not reach past the backup", window, err)
	}
	t.Logf("restore window %s", window)
	lab.restoreAndCount(ctx, t, "p", point, 150)
	lab.restoreAndCount(ctx, t, "l", window.Latest, 185)
}

type mocoLab struct {
	client    *k8s.Client
	mysql     *provisioner.MySQLProvisioner
	adapter   *MySQLBackupAdapter
	inst      *domain.DatabaseInstance
	project   string
	namespace string
	plan      config.TierConfig
	store     *domain.S3Credentials
}

func newMOCOLab(t *testing.T, client *k8s.Client, instances int) *mocoLab {
	t.Helper()
	ctx := context.Background()
	suffix := time.Now().UnixNano() % 1_000_000
	lab := &mocoLab{client: client, mysql: provisioner.NewMySQLProvisioner(client), project: fmt.Sprintf("it587-%d", suffix),
		plan: config.TierConfig{Instances: instances, StorageSize: "2Gi", CPU: "0.5", Memory: "1Gi"}}
	lab.namespace = "org1-" + lab.project
	lab.mysql.SetBackupPathStyle(true)
	lab.store = &domain.S3Credentials{Bucket: os.Getenv("MOCO_IT_BUCKET"), Endpoint: os.Getenv("MOCO_IT_S3_CLUSTER"),
		Region: "us-east-1", AccessKeyID: os.Getenv("MOCO_IT_S3_KEY"), SecretAccessKey: os.Getenv("MOCO_IT_S3_SECRET")}
	t.Cleanup(func() { lab.cleanup(t, lab.namespace) })
	result, err := lab.mysql.Provision(ctx, domain.ProvisioningRequest{ProjectName: lab.project, OrgID: "org1", DBType: domain.MySQL,
		DatabaseName: "app", MySQLVersion: config.DefaultMySQLVersion()}, lab.plan, func(domain.ProvisioningStage) {})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	lab.writeStaticAccess(t, lab.namespace, k8s.MySQLBackupAccessName)
	schedule, _ := config.MySQLBackupSchedule(domain.Standard)
	if err := lab.mysql.AttachBackups(ctx, k8s.MySQLBackupOpts{ProjectID: lab.project, Namespace: lab.namespace, Schedule: schedule,
		Plan: lab.plan, Disk: lab.plan.StorageSize, Store: lab.mysql.BackupStore(lab.store)}); err != nil {
		t.Fatalf("AttachBackups: %v", err)
	}
	host := *lab.store
	host.Endpoint = os.Getenv("MOCO_IT_S3_HOST")
	lab.adapter = NewMySQLBackupAdapter(MySQLBackupAdapterConfig{
		Kube: client, MySQL: lab.mysql, Storage: StaticBackupStorage(lab.store), Records: &fakeBackupRecordStore{},
		OpenStore: func(ctx context.Context, _ *domain.S3Credentials) (ObjectDeleter, error) {
			return AWSObjectDeleterFactory(true)(ctx, &host)
		},
	})
	lab.adapter.SetPodStreamer(client)
	lab.adapter.SetReadyPoller(provisioner.NewPoller(5*time.Second, 15*time.Minute))
	enabled := true
	lab.inst = &domain.DatabaseInstance{ProjectID: lab.project, OrgID: "org1", Namespace: result.Namespace, DBType: domain.MySQL,
		DeploymentMode: domain.ModeK8s, Status: string(domain.StatusActive), Tier: domain.Standard, BackupEnabled: &enabled}
	return lab
}

// writeStaticAccess gives the jobs the bucket's own keys: the local store
// issues no temporary credentials.
func (lab *mocoLab) writeStaticAccess(t *testing.T, namespace, name string) {
	t.Helper()
	ctx := context.Background()
	data := map[string][]byte{"AWS_ACCESS_KEY_ID": []byte(lab.store.AccessKeyID), "AWS_SECRET_ACCESS_KEY": []byte(lab.store.SecretAccessKey),
		"AWS_REGION": []byte(lab.store.Region)}
	if err := lab.client.CreateSecret(ctx, namespace, name, data); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if err := lab.client.ApplyCRD(ctx, k8s.ServiceAccountGVR, namespace, k8s.BuildMySQLBackupServiceAccount(namespace)); err != nil {
		t.Fatal(err)
	}
}

// sql runs statements on the primary and commits: MOCO's client
// configuration turns autocommit off.
func (lab *mocoLab) sql(ctx context.Context, t *testing.T, namespace, project string, statements ...string) string {
	t.Helper()
	cluster, err := lab.client.GetCRD(ctx, k8s.MOCOClusterGVR, namespace, k8s.MySQLClusterName(project))
	if err != nil {
		t.Fatal(err)
	}
	out, err := lab.client.ExecInPodStdin(ctx, namespace, k8s.MySQLPrimaryPod(cluster), provisioner.MOCOMySQLContainer,
		[]string{"mysql", "--defaults-extra-file=" + provisioner.MOCOAdminDefaultsFile, "--batch", "--skip-column-names"},
		strings.Join(statements, ";\n")+";\nCOMMIT;\n")
	if err != nil {
		t.Fatalf("%v: %v", statements, err)
	}
	return strings.TrimSpace(out)
}

// backup runs a backup now and waits for MOCO to record it. A replica that
// was just cloned can refuse the first job's connection, so it tries thrice.
func (lab *mocoLab) backup(ctx context.Context, t *testing.T) {
	t.Helper()
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if err = lab.backupOnce(ctx, t); err == nil {
			return
		}
		t.Logf("backup attempt %d: %v", attempt, err)
	}
	t.Fatalf("backup: %v", err)
}

func (lab *mocoLab) backupOnce(ctx context.Context, t *testing.T) error {
	ref, err := lab.adapter.TriggerManual(ctx, lab.inst)
	if err != nil {
		t.Fatalf("TriggerManual: %v", err)
	}
	return lab.adapter.poller.WaitUntilReady(ctx, "backup", func(ctx context.Context) (bool, error) {
		status, err := lab.adapter.BackupStatusOf(ctx, lab.inst, ref.ID)
		if status == backupStatusFailed {
			return false, fmt.Errorf("backup %s failed", ref.ID)
		}
		if err != nil || status != backupStatusCompleted {
			return false, nil
		}
		cluster, err := lab.client.GetCRD(ctx, k8s.MOCOClusterGVR, lab.namespace, k8s.MySQLClusterName(lab.project))
		if err != nil {
			return false, nil
		}
		_, recorded := mysqlBackupSourceOf(cluster)
		return recorded, nil
	})
}

func (lab *mocoLab) requireHealthy(ctx context.Context, t *testing.T) {
	t.Helper()
	if lab.plan.Instances == 1 {
		return
	}
	err := lab.adapter.poller.WaitUntilReady(ctx, "healthy", func(ctx context.Context) (bool, error) {
		cluster, err := lab.client.GetCRD(ctx, k8s.MOCOClusterGVR, lab.namespace, k8s.MySQLClusterName(lab.project))
		return err == nil && k8s.MySQLClusterHealthy(cluster), nil
	})
	if err != nil {
		t.Fatalf("the source is not healthy after the ships: %v", err)
	}
}

// restoreAndCount restores into a new namespace at point as Restore does,
// with the bucket's own keys, and counts the rows.
func (lab *mocoLab) restoreAndCount(ctx context.Context, t *testing.T, tag string, point time.Time, want int) {
	t.Helper()
	backups, marks, err := lab.adapter.catalog(ctx, lab.inst)
	if err != nil {
		t.Fatal(err)
	}
	mocoPoint, tail, err := mysqlRestoreAt(backups, marks, point)
	if err != nil {
		t.Fatalf("plan the restore at %s: %v", point, err)
	}
	if tail == nil {
		t.Fatalf("a point past the backup needs the shipped binlogs")
	}
	project := lab.project + tag
	namespace := "org1-" + project
	t.Cleanup(func() { lab.cleanup(t, namespace) })
	if err := lab.client.CreateProjectNamespace(ctx, namespace, "org1"); err != nil {
		t.Fatal(err)
	}
	lab.writeStaticAccess(t, namespace, k8s.MySQLRestoreSourceAccessName)
	image, _ := config.MySQLImage(config.DefaultMySQLVersion())
	opts := k8s.MySQLClusterOpts{ProjectID: project, Namespace: namespace, Tier: config.TierConfig{Instances: 1, StorageSize: "2Gi", CPU: "0.5", Memory: "1Gi"}, Image: image}
	source := k8s.MySQLRestoreSource{Namespace: lab.namespace, ProjectID: lab.project, Point: mocoPoint, CredentialsSecret: k8s.MySQLRestoreSourceAccessName}
	job := k8s.MySQLBackupOpts{ProjectID: project, Namespace: namespace, Plan: opts.Tier, Disk: "2Gi", Store: lab.mysql.BackupStore(lab.store)}
	if err := lab.mysql.ApplyRestoredCluster(ctx, opts, source, job); err != nil {
		t.Fatalf("ApplyRestoredCluster: %v", err)
	}
	if err := lab.mysql.AwaitRestored(ctx, lab.adapter.poller, namespace, project, 1); err != nil {
		t.Fatalf("AwaitRestored: %v", err)
	}
	if err := lab.adapter.replayTail(ctx, namespace, project, tail); err != nil {
		t.Fatalf("replayTail: %v", err)
	}
	if got := lab.sql(ctx, t, namespace, project, "SELECT COUNT(*) FROM app.notes"); got != strconv.Itoa(want) {
		t.Fatalf("restored to %s: %s rows, want %d", point, got, want)
	}
	t.Logf("restored to %s: %d rows", point.Format(time.RFC3339), want)
}

// cleanup removes a namespace, or keeps it to read after a failure.
func (lab *mocoLab) cleanup(t *testing.T, namespace string) {
	if t.Failed() {
		t.Logf("kept %s to inspect", namespace)
		return
	}
	_ = lab.client.DeleteNamespace(context.Background(), namespace)
}

func insertMOCONotes(count int) string {
	return fmt.Sprintf("INSERT INTO app.notes (body) SELECT 'note' FROM (WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < %d) SELECT i FROM n) rows_", count)
}
