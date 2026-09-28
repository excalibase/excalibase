//go:build live

package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/objectcreds"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// The plugin exactly as the charts repo installs it: this version, these digests.
const (
	barmanPluginVersion  = "v0.15.0"
	barmanPluginManifest = "https://github.com/cloudnative-pg/plugin-barman-cloud/releases/download/" + barmanPluginVersion + "/manifest.yaml"
	barmanPluginImage    = "ghcr.io/cloudnative-pg/plugin-barman-cloud:" + barmanPluginVersion
	barmanPluginDigest   = "sha256:563c680fe7fda3466ca2b1f55a1397ed2ddc9e760360107dd7724f1959c1a536"
	barmanSidecarImage   = "ghcr.io/cloudnative-pg/plugin-barman-cloud-sidecar:" + barmanPluginVersion +
		"@sha256:06c78deca670525daa35fb1e5323159092785d11cf87b86217bdd5c679a41a84"
	backupLiveMinIOImage = "docker.io/pgsty/minio:RELEASE.2026-08-04T00-00-00Z@sha256:b6bfe7239bfc83fb90d31612d9704d86039dd714f7904b3f1ad68f211e602372"
	backupLiveOrg        = "org1"
	backupLiveBucket     = "excalibase-backups"
	backupLiveNodePort   = 30900
	backupLiveSchedule   = "0 0 2 * * *"
)

// archiverStatus: last archived, archived count, failed count, last failed
// segment, and whether the most recent attempt succeeded.
const archiverStatus = `SELECT coalesce(last_archived_wal, '-') || ' ' || archived_count || ' ' || failed_count || ' ' ||
coalesce(last_failed_wal, '-') || ' ' || (last_failed_time IS NULL OR last_failed_time < last_archived_time)
FROM pg_stat_archiver`

var sidecarSecretBlock = regexp.MustCompile(`(?m)^  SIDECAR_IMAGE: \|\n(?:    .*\n)+`)

type backupLab struct {
	*documentDBLab
	// store is the platform key; only minted credentials reach a namespace.
	store     *domain.S3Credentials
	issuer    *BackupCredentialIssuer
	ttl       time.Duration
	suffix    string
	instances storage.InstanceStore
	adapter   *K8sBackupAdapter
	source    *domain.DatabaseInstance
	sourceID  string
}

// Backups, WAL archiving and restores run on temporary credentials minted per
// project (EXC-476). With R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY, R2_ENDPOINT
// and R2_BUCKET set the store is that R2 bucket and credentials live 4 minutes,
// so the run crosses a renewal and an expiry; otherwise an in-cluster MinIO
// issues STS sessions (15 minutes, too long to wait out here).
//
// Run with: go test ./internal/service/ -tags=live -run TestLiveBackupsRunThroughTheBarmanCloudPlugin -v -count=1 -timeout 90m
func TestLiveBackupsRunThroughTheBarmanCloudPlugin(t *testing.T) {
	lab := &backupLab{documentDBLab: startDocumentDBLab(t), suffix: randomHex(t, 3)}
	lab.sourceID = "bkpsrc" + lab.suffix
	lab.installBackupStack(t)
	t.Cleanup(func() { lab.purgePrefixes(t) })
	lab.provisionSource(t)
	lab.expectNoPlatformKeyIn(t, lab.sourceID)
	stopRenewal := lab.startRenewal(t)
	defer func() { stopRenewal() }()

	lab.write(t, lab.sourceID, 1, 1000)
	atBackup := lab.checksums(t, lab.sourceID)
	backupID := lab.takeBackup(t)
	lab.expectArchivingSurvivesExpiry(t)

	lab.write(t, lab.sourceID, 1001, 3000)
	beforeLoss := lab.checksums(t, lab.sourceID)
	time.Sleep(1100 * time.Millisecond)
	targetTime := strings.TrimSpace(lab.psql(t, lab.sourceID, "SELECT to_char(date_trunc('second', clock_timestamp() AT TIME ZONE 'UTC'), 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"')"))
	time.Sleep(1500 * time.Millisecond)
	lab.psql(t, lab.sourceID, "DROP TABLE orders; DELETE FROM customers;")
	lab.expectArchivedThroughNow(t, lab.sourceID)
	t.Logf("at backup %v, before loss %v (target %s)", atBackup, beforeLoss, targetTime)

	byID := lab.restore(t, "bkpbyid"+lab.suffix, domain.RestoreRequest{BackupID: backupID})
	lab.expectNoPlatformKeyIn(t, byID)
	lab.expectConfinedToItsPrefix(t, byID, lab.sourceID)
	if got := lab.checksums(t, byID); got != atBackup {
		t.Fatalf("restore by backup id: got %v, want the rows at the backup %v", got, atBackup)
	}
	lab.expectArchivedThroughNow(t, byID)
	lab.expectScheduledBackupCompleted(t, byID)

	at, err := time.Parse(time.RFC3339, targetTime)
	if err != nil {
		t.Fatalf("parse target time: %v", err)
	}
	pitr := lab.restore(t, "bkppitr"+lab.suffix, domain.RestoreRequest{TargetTime: &domain.ZonedTime{Time: at}})
	if got := lab.checksums(t, pitr); got != beforeLoss {
		t.Fatalf("point-in-time restore: got %v, want the rows before the loss %v", got, beforeLoss)
	}
	stopRenewal()
	stopRenewal = func() {}
	lab.expectArchivingFailsLoudlyOnceExpired(t, pitr)
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random: %v", err)
	}
	return hex.EncodeToString(b)
}

// installBackupStack installs CloudNativePG, cert-manager, the pinned plugin and an in-cluster MinIO.
func (lab *backupLab) installBackupStack(t *testing.T) {
	t.Helper()
	lab.kubectl(t, "apply", "--server-side", "-f", operatorURLs[domain.PostgreSQL])
	lab.kubectl(t, "apply", "-f", certManagerManifest)
	lab.kubectl(t, "wait", "--for=condition=available", "--timeout=300s", "deployment/cnpg-controller-manager", "-n", "cnpg-system")
	lab.kubectl(t, "wait", "--for=condition=available", "--timeout=300s", "deployment", "--all", "-n", "cert-manager")
	lab.applyFile(t, "/tmp/barman-cloud.yaml", pinnedPluginManifest(t))
	lab.kubectl(t, "wait", "--for=condition=Ready", "--timeout=300s", "certificate/barman-cloud-server", "certificate/barman-cloud-client", "-n", "cnpg-system")
	lab.kubectl(t, "rollout", "status", "deployment/barman-cloud", "-n", "cnpg-system", "--timeout=300s")

	var minter objectcreds.Minter
	if r2 := r2StoreFromEnv(); r2 != nil {
		lab.store, minter, lab.ttl = r2, objectcreds.R2Signer{}, 4*time.Minute
	} else {
		user, password := "backup"+randomHex(t, 4), randomHex(t, 24)
		lab.applyFile(t, "/tmp/minio.yaml", minioManifest(user, password))
		lab.kubectl(t, "rollout", "status", "deployment/minio", "-n", "backup-store", "--timeout=300s")
		if lab.container == nil {
			t.Fatal("the MinIO store needs the k3s container lab; set R2_* to run against an existing cluster")
		}
		nodeIP, err := lab.container.ContainerIP(lab.ctx)
		if err != nil {
			t.Fatalf("node ip: %v", err)
		}
		// One address both this process (minting) and the pods (archiving) reach.
		lab.store = &domain.S3Credentials{AccessKeyID: user, SecretAccessKey: password, Bucket: backupLiveBucket,
			Endpoint: fmt.Sprintf("http://%s:%d", nodeIP, backupLiveNodePort), Region: "us-east-1"}
		minter, lab.ttl = objectcreds.STSMinter{}, 15*time.Minute
	}
	issuer, err := NewBackupCredentialIssuer(BackupCredentialIssuerConfig{
		Minter: minter, OpenStore: AWSObjectDeleterFactory(true), TTL: lab.ttl, SourceTTL: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("issuer: %v", err)
	}
	lab.issuer = issuer
	lab.instances = emptyInstanceStore(t)
	t.Logf("backup store %s, credentials live %s", lab.store.Endpoint, lab.ttl)
}

func r2StoreFromEnv() *domain.S3Credentials {
	store := &domain.S3Credentials{
		AccessKeyID: os.Getenv("R2_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("R2_SECRET_ACCESS_KEY"),
		Endpoint: os.Getenv("R2_ENDPOINT"), Bucket: os.Getenv("R2_BUCKET"), Region: "auto",
	}
	if store.AccessKeyID == "" || store.SecretAccessKey == "" || store.Endpoint == "" || store.Bucket == "" {
		return nil
	}
	return store
}

func (lab *backupLab) applyFile(t *testing.T, path, manifest string) {
	t.Helper()
	if lab.container == nil {
		local := filepath.Join(t.TempDir(), filepath.Base(path))
		if err := os.WriteFile(local, []byte(manifest), 0o600); err != nil {
			t.Fatalf("write %s: %v", local, err)
		}
		eventuallyLive(t, "apply "+path+" once its webhooks answer", 3*time.Minute, func() bool {
			_, err := lab.kubectlOutput("apply", "--server-side", "--force-conflicts", "-f", local)
			return err == nil
		})
		return
	}
	if err := lab.container.CopyToContainer(lab.ctx, []byte(manifest), path, 0o644); err != nil {
		t.Fatalf("copy %s: %v", path, err)
	}
	eventuallyLive(t, "apply "+path+" once its webhooks answer", 3*time.Minute, func() bool {
		code, _, err := lab.container.Exec(lab.ctx, []string{"kubectl", "apply", "--server-side", "--force-conflicts", "-f", path})
		return err == nil && code == 0
	})
}

func pinnedPluginManifest(t *testing.T) string {
	t.Helper()
	resp, err := http.Get(barmanPluginManifest)
	if err != nil {
		t.Fatalf("download plugin manifest: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("download plugin manifest: HTTP %d %v", resp.StatusCode, err)
	}
	manifest := string(raw)
	if strings.Count(manifest, "image: "+barmanPluginImage+"\n") != 1 || len(sidecarSecretBlock.FindAllString(manifest, -1)) != 1 {
		t.Fatal("the plugin manifest no longer has the image lines this test pins")
	}
	manifest = strings.Replace(manifest, "image: "+barmanPluginImage+"\n", "image: "+barmanPluginImage+"@"+barmanPluginDigest+"\n", 1)
	return sidecarSecretBlock.ReplaceAllString(manifest, "  SIDECAR_IMAGE: "+base64.StdEncoding.EncodeToString([]byte(barmanSidecarImage))+"\n")
}

func minioManifest(user, password string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata: {name: backup-store}
---
apiVersion: v1
kind: Secret
metadata: {name: minio-root, namespace: backup-store}
stringData: {user: %q, password: %q, bucket: %q}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: minio, namespace: backup-store}
spec:
  selector: {matchLabels: {app: minio}}
  template:
    metadata: {labels: {app: minio}}
    spec:
      containers:
        - name: minio
          image: %s
          command: ["/bin/sh", "-c"]
          args:
            - |
              minio server /data --address :9000 &
              until mc alias set local http://127.0.0.1:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" > /dev/null 2>&1; do sleep 1; done
              mc mb --ignore-existing "local/$BUCKET"
              wait
          env:
            - {name: MINIO_ROOT_USER, valueFrom: {secretKeyRef: {name: minio-root, key: user}}}
            - {name: MINIO_ROOT_PASSWORD, valueFrom: {secretKeyRef: {name: minio-root, key: password}}}
            - {name: BUCKET, valueFrom: {secretKeyRef: {name: minio-root, key: bucket}}}
            - {name: MC_CONFIG_DIR, value: /tmp/.mc}
          readinessProbe:
            exec: {command: ["/bin/sh", "-c", "mc ls \"local/$BUCKET\" > /dev/null"]}
            periodSeconds: 3
          volumeMounts: [{name: data, mountPath: /data}]
      volumes: [{name: data, emptyDir: {}}]
---
apiVersion: v1
kind: Service
metadata: {name: minio, namespace: backup-store}
spec:
  selector: {app: minio}
  type: NodePort
  ports: [{port: 9000, targetPort: 9000, nodePort: %d}]
`, user, password, backupLiveBucket, backupLiveMinIOImage, backupLiveNodePort)
}

func backupLiveTier() config.TierConfig {
	return config.TierConfig{Instances: 1, StorageSize: "1Gi", Memory: "512Mi", CPU: "0.5", StatementTimeout: "5s", BackupEnabled: true}
}

// provisionSource runs the platform's own provisioner with backups on, on the catalogue image.
func (lab *backupLab) provisionSource(t *testing.T) {
	t.Helper()
	minted, err := lab.issuer.ForNewProject(lab.ctx, lab.store, lab.sourceID)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	req := domain.ProvisioningRequest{
		ProjectName: lab.sourceID, OrgID: backupLiveOrg, DBType: domain.PostgreSQL, PostgresVersion: "17",
		Backup: &domain.BackupSettings{Enabled: true, Schedule: backupLiveSchedule, Retention: 7, S3: minted},
	}
	creds, err := provisioner.NewPostgreSQLProvisioner(lab.client, "").
		ProvisionWithRollback(lab.ctx, req, backupLiveTier(), provisioner.NewProvisionContext(nil, nil))
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	enabled := true
	lab.source = &domain.DatabaseInstance{
		ProjectID: lab.sourceID, OrgID: backupLiveOrg, Namespace: backupLiveOrg + "-" + lab.sourceID,
		DBType: domain.PostgreSQL, PostgresVersion: "17", DeploymentMode: domain.ModeK8s, Status: "ACTIVE",
		DatabaseName: creds.DatabaseName, Username: creds.Username, Password: creds.Password, BackupEnabled: &enabled,
	}
	if err := lab.instances.Create(lab.source); err != nil {
		t.Fatalf("register source: %v", err)
	}
	lab.adapter = NewK8sBackupAdapter(lab.client, StaticBackupStorage(lab.store))
	lab.adapter.SetBackupCredentials(lab.issuer)
	lab.psql(t, lab.sourceID, "CREATE TABLE customers (id int PRIMARY KEY, name text NOT NULL);"+
		"CREATE TABLE orders (id int PRIMARY KEY, customer_id int REFERENCES customers(id), amount numeric(10,2));")
}

func (lab *backupLab) psql(t *testing.T, project, sql string) string {
	t.Helper()
	out, err := lab.client.ExecInPod(lab.ctx, backupLiveOrg+"-"+project, project+"-postgres-1", "postgres",
		[]string{"psql", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "app", "-tAc", sql})
	if err != nil {
		t.Fatalf("psql on %s: %v\n%s", project, err, out)
	}
	return out
}

func (lab *backupLab) write(t *testing.T, project string, from, to int) {
	t.Helper()
	lab.psql(t, project, fmt.Sprintf("INSERT INTO customers SELECT g, 'customer-' || g FROM generate_series(%d, %d) g;"+
		"INSERT INTO orders SELECT g, g, round((g * 13.37)::numeric %% 999, 2) FROM generate_series(%d, %d) g;", from, to, from, to))
}

// checksums is each table's row count and the md5 of every row in key order.
func (lab *backupLab) checksums(t *testing.T, project string) string {
	t.Helper()
	sums := []string{}
	for _, table := range []string{"customers", "orders"} {
		sums = append(sums, table+"="+strings.TrimSpace(lab.psql(t, project,
			"SELECT count(*) || ':' || md5(coalesce(string_agg(t::text, '|' ORDER BY t.id), '')) FROM "+table+" t")))
	}
	return strings.Join(sums, " ")
}

func (lab *backupLab) takeBackup(t *testing.T) string {
	t.Helper()
	started := time.Now()
	ref, err := lab.adapter.TriggerManual(lab.ctx, lab.source)
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	var last BackupRef
	eventuallyLive(t, "backup "+ref.ID+" completed", 10*time.Minute, func() bool {
		refs, err := lab.adapter.List(lab.ctx, lab.source)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, candidate := range refs {
			if candidate.ID == ref.ID {
				last = candidate
			}
		}
		if last.Status == backupStatusFailed {
			t.Fatalf("backup failed: %+v", last)
		}
		return last.Status == backupStatusCompleted
	})
	t.Logf("base backup %s completed in %s", ref.ID, time.Since(started).Round(time.Second))
	return ref.ID
}

// expectArchivedThroughNow switches out the current segment and waits for the plugin to archive it.
func (lab *backupLab) expectArchivedThroughNow(t *testing.T, project string) {
	t.Helper()
	segment := strings.TrimSpace(lab.psql(t, project, "SELECT pg_walfile_name(pg_current_wal_lsn())"))
	lab.psql(t, project, "SELECT pg_switch_wal()")
	started := time.Now()
	var status string
	for deadline := time.Now().Add(5 * time.Minute); ; time.Sleep(5 * time.Second) {
		status = strings.TrimSpace(lab.psql(t, project, archiverStatus))
		fields := strings.Fields(status)
		if len(fields) == 5 && fields[0] >= segment && fields[4] == "true" {
			break
		}
		if time.Now().After(deadline) {
			lab.dumpBackupState(t, project)
			t.Fatalf("%s never archived %s (%s)", project, segment, status)
		}
	}
	t.Logf("%s: archived through %s in %s (%s)", project, segment, time.Since(started).Round(time.Second), status)
}

func (lab *backupLab) dumpBackupState(t *testing.T, project string) {
	t.Helper()
	namespace := backupLiveOrg + "-" + project
	for _, args := range [][]string{
		{"get", "pods", "-n", namespace, "-o", "wide"},
		{"get", "clusters.postgresql.cnpg.io,objectstores.barmancloud.cnpg.io,backups.postgresql.cnpg.io", "-n", namespace, "-o", "yaml"},
		{"logs", "-n", namespace, project + "-postgres-1", "-c", "plugin-barman-cloud", "--tail=80"},
		{"logs", "-n", namespace, project + "-postgres-1", "-c", "postgres", "--tail=80"},
		{"logs", "-n", "cnpg-system", "deployment/barman-cloud", "--tail=80"},
	} {
		out, err := lab.kubectlOutput(args...)
		t.Logf("kubectl %v (%v):\n%s", args, err, out)
	}
}

// expectScheduledBackupCompleted waits for the restored project's first
// scheduled backup, which is taken as soon as its schedule exists.
func (lab *backupLab) expectScheduledBackupCompleted(t *testing.T, project string) {
	t.Helper()
	inst := &domain.DatabaseInstance{ProjectID: project, Namespace: backupLiveOrg + "-" + project}
	var refs []BackupRef
	eventuallyLive(t, project+" took its first scheduled backup", 10*time.Minute, func() bool {
		var err error
		refs, err = lab.adapter.List(lab.ctx, inst)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, ref := range refs {
			if ref.Type == backupTypeScheduled && ref.Status == backupStatusFailed {
				t.Fatalf("scheduled backup failed: %+v", ref)
			}
			if ref.Type == backupTypeScheduled && ref.Status == backupStatusCompleted {
				return true
			}
		}
		return false
	})
	t.Logf("%s: first scheduled backup completed (%+v)", project, refs)
}

type psqlProbe struct{ lab *backupLab }

func (p psqlProbe) Probe(ctx context.Context, projectID string) error {
	_, err := p.lab.client.ExecInPod(ctx, backupLiveOrg+"-"+projectID, projectID+"-postgres-1", "postgres",
		[]string{"psql", "-U", "postgres", "-d", "app", "-tAc", "SELECT 1"})
	return err
}

func (lab *backupLab) restore(t *testing.T, project string, req domain.RestoreRequest) string {
	t.Helper()
	lab.adapter.SetInstanceStore(lab.instances)
	lab.adapter.SetProjectRegistrar(&fakeRegistrar{store: lab.instances})
	lab.adapter.SetDatabaseProbe(psqlProbe{lab: lab})
	lab.adapter.SetRestorePlanSource(&fakeRestorePlans{plan: RestorePlan{
		Tier: domain.Free, Config: backupLiveTier(),
		Backup: &domain.BackupSettings{Enabled: true, Schedule: backupLiveSchedule, Retention: 7},
	}})
	req.NewProjectName, req.TargetProjectID = project, project
	started := time.Now()
	if _, err := lab.adapter.Restore(lab.ctx, lab.source, req); err != nil {
		t.Fatalf("restore %s: %v", project, err)
	}
	t.Logf("restore %s (%s) usable in %s", project, kindOf(req), time.Since(started).Round(time.Second))
	return project
}

func kindOf(req domain.RestoreRequest) string {
	kind, value := req.RestoreTargetKind()
	return kind + " " + value
}
