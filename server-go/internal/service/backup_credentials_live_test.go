//go:build live

package service

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const liveRenewEvery = 15 * time.Second

func (lab *backupLab) namespaceOf(project string) string { return backupLiveOrg + "-" + project }

// expectNoPlatformKeyIn proves no Secret in the project's namespace holds the
// platform key's secret, raw or encoded.
func (lab *backupLab) expectNoPlatformKeyIn(t *testing.T, project string) {
	t.Helper()
	secrets, err := lab.cs.CoreV1().Secrets(lab.namespaceOf(project)).List(lab.ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list secrets: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(lab.store.SecretAccessKey))
	for _, secret := range secrets.Items {
		for key, value := range secret.Data {
			if strings.Contains(string(value), lab.store.SecretAccessKey) || strings.Contains(string(value), encoded) {
				t.Fatalf("%s/%s[%s] holds the platform key", secret.Namespace, secret.Name, key)
			}
		}
	}
	t.Logf("%s: %d secrets, none holds the platform key", lab.namespaceOf(project), len(secrets.Items))
}

func (lab *backupLab) startRenewal(t *testing.T) func() {
	t.Helper()
	renewer := NewBackupCredentialRenewer(BackupCredentialRenewerConfig{
		Instances: lab.instances, Kube: lab.client, Storage: StaticBackupStorage(lab.store), Issuer: lab.issuer,
	})
	return renewer.Start(lab.ctx, NewLeadership(AlwaysLeader{}), liveRenewEvery)
}

func (lab *backupLab) credential(t *testing.T, project, name string) map[string][]byte {
	t.Helper()
	data, err := lab.client.GetSecret(lab.ctx, lab.namespaceOf(project), name)
	if err != nil {
		t.Fatalf("read %s of %s: %v", name, project, err)
	}
	return data
}

func (lab *backupLab) podIdentity(t *testing.T, project string) string {
	t.Helper()
	pod, err := lab.cs.CoreV1().Pods(lab.namespaceOf(project)).Get(lab.ctx, project+"-postgres-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("pod: %v", err)
	}
	restarts := 0
	for _, status := range pod.Status.ContainerStatuses {
		restarts += int(status.RestartCount)
	}
	return string(pod.UID) + "/restarts=" + strconv.Itoa(restarts)
}

func waitPast(t *testing.T, what string, at time.Time) {
	t.Helper()
	wait := time.Until(at) + 20*time.Second
	t.Logf("waiting %s for %s", wait.Round(time.Second), what)
	time.Sleep(wait)
}

// expectArchivingSurvivesExpiry waits out the credential the project was
// created with and proves WAL and a base backup still reach the store, on the
// renewed Secret, with the same pod.
func (lab *backupLab) expectArchivingSurvivesExpiry(t *testing.T) {
	t.Helper()
	if lab.ttl > 5*time.Minute {
		t.Logf("credentials live %s: renewal across expiry not waited out on this store", lab.ttl)
		return
	}
	first := lab.credential(t, lab.sourceID, k8s.BackupCredentialsSecretName)
	expiry, err := k8s.BackupCredentialsExpiry(first)
	if err != nil {
		t.Fatalf("expiry: %v", err)
	}
	pod := lab.podIdentity(t, lab.sourceID)
	waitPast(t, "the first credential to expire", expiry)

	renewed := lab.credential(t, lab.sourceID, k8s.BackupCredentialsSecretName)
	if string(renewed[k8s.BackupCredentialsSessionTokenKey]) == string(first[k8s.BackupCredentialsSessionTokenKey]) {
		t.Fatal("the credential was not renewed before it expired")
	}
	newExpiry, _ := k8s.BackupCredentialsExpiry(renewed)
	t.Logf("renewed: first expired %s, current expires %s", expiry.Format(time.RFC3339), newExpiry.Format(time.RFC3339))

	lab.write(t, lab.sourceID, 5001, 5100)
	lab.expectArchivedThroughNow(t, lab.sourceID)
	lab.takeBackup(t)
	lab.psql(t, lab.sourceID, "DELETE FROM orders WHERE id > 5000; DELETE FROM customers WHERE id > 5000;")
	if now := lab.podIdentity(t, lab.sourceID); now != pod {
		t.Fatalf("the pod changed across renewal: %s -> %s", pod, now)
	}
	t.Logf("archived and backed up after the first credential expired, same pod (%s)", pod)
}

func (lab *backupLab) s3With(data map[string][]byte) *s3.Client {
	return s3.New(s3.Options{
		Region:       lab.store.Region,
		BaseEndpoint: aws.String(lab.store.Endpoint),
		UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(string(data["ACCESS_KEY_ID"]),
			string(data["ACCESS_SECRET_KEY"]), string(data[k8s.BackupCredentialsSessionTokenKey])),
	})
}

func forbidden(err error) bool {
	var respErr *smithyhttp.ResponseError
	return errors.As(err, &respErr) && respErr.HTTPStatusCode() == http.StatusForbidden
}

// expectConfinedToItsPrefix uses the credentials a project's namespace holds
// against another project's prefix: every read, list and write is refused,
// while its own prefix works.
func (lab *backupLab) expectConfinedToItsPrefix(t *testing.T, project, other string) {
	t.Helper()
	ctx := lab.ctx
	bucket := aws.String(lab.store.Bucket)
	own := lab.s3With(lab.credential(t, project, k8s.BackupCredentialsSecretName))
	listing, err := own.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: bucket, Prefix: aws.String(k8s.BarmanObjectPrefix(project))})
	if err != nil || len(listing.Contents) == 0 {
		t.Fatalf("%s cannot list its own prefix: %v", project, err)
	}
	platform := lab.s3With(map[string][]byte{"ACCESS_KEY_ID": []byte(lab.store.AccessKeyID), "ACCESS_SECRET_KEY": []byte(lab.store.SecretAccessKey)})
	others, err := platform.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: bucket, Prefix: aws.String(k8s.BarmanObjectPrefix(other)), MaxKeys: aws.Int32(1)})
	if err != nil || len(others.Contents) == 0 {
		t.Fatalf("platform key cannot find an object of %s: %v", other, err)
	}
	victim := others.Contents[0].Key
	if _, err := own.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: bucket, Prefix: aws.String(k8s.BarmanObjectPrefix(other))}); !forbidden(err) {
		t.Fatalf("%s listed %s's prefix: %v", project, other, err)
	}
	if _, err := own.GetObject(ctx, &s3.GetObjectInput{Bucket: bucket, Key: victim}); !forbidden(err) {
		t.Fatalf("%s read %s: %v", project, aws.ToString(victim), err)
	}
	if _, err := own.PutObject(ctx, &s3.PutObjectInput{Bucket: bucket, Key: aws.String(k8s.BarmanObjectPrefix(other) + "planted"), Body: strings.NewReader("x")}); !forbidden(err) {
		t.Fatalf("%s wrote into %s: %v", project, other, err)
	}
	if _, err := own.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: bucket, Key: victim}); !forbidden(err) {
		t.Fatalf("%s deleted %s: %v", project, aws.ToString(victim), err)
	}
	if _, err := own.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: bucket}); !forbidden(err) {
		t.Fatalf("%s listed the whole bucket: %v", project, err)
	}
	source := lab.s3With(lab.credential(t, project, k8s.RecoverySourceCredentialsSecretName))
	if out, err := source.GetObject(ctx, &s3.GetObjectInput{Bucket: bucket, Key: victim}); err != nil {
		t.Fatalf("the restore's source credential cannot read its source: %v", err)
	} else {
		_, _ = io.Copy(io.Discard, out.Body)
		_ = out.Body.Close()
	}
	if _, err := source.PutObject(ctx, &s3.PutObjectInput{Bucket: bucket, Key: aws.String(k8s.BarmanObjectPrefix(other) + "planted"), Body: strings.NewReader("x")}); !forbidden(err) {
		t.Fatalf("the restore's source credential could write to its source: %v", err)
	}
	t.Logf("%s: own prefix readable; %s's prefix refused for list/get/put/delete; bucket listing refused; source credential read-only", project, other)
}

// expectArchivingFailsLoudlyOnceExpired stops renewal and proves an expired
// credential shows up as failed archiving, not as silence.
func (lab *backupLab) expectArchivingFailsLoudlyOnceExpired(t *testing.T, project string) {
	t.Helper()
	if lab.ttl > 5*time.Minute {
		t.Logf("credentials live %s: expiry without renewal not waited out on this store", lab.ttl)
		return
	}
	expiry, err := k8s.BackupCredentialsExpiry(lab.credential(t, project, k8s.BackupCredentialsSecretName))
	if err != nil {
		t.Fatalf("expiry: %v", err)
	}
	waitPast(t, project+"'s unrenewed credential to expire", expiry)
	lab.psql(t, project, "INSERT INTO customers SELECT g, 'late-' || g FROM generate_series(900001, 900100) g")
	lab.psql(t, project, "SELECT pg_switch_wal()")
	cluster := project + postgresClusterSuffix
	var status, condition string
	eventuallyLive(t, project+" reports failed archiving", 5*time.Minute, func() bool {
		status = strings.TrimSpace(lab.psql(t, project, archiverStatus))
		fields := strings.Fields(status)
		obj, err := lab.client.GetCRD(context.Background(), k8s.CNPGClusterGVR, lab.namespaceOf(project), cluster)
		if err == nil {
			conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
			for _, raw := range conditions {
				entry, _ := raw.(map[string]interface{})
				if entry["type"] == "ContinuousArchiving" {
					condition = entry["status"].(string) + " " + stringOf(entry["reason"]) + " " + stringOf(entry["message"])
				}
			}
		}
		return len(fields) == 5 && fields[2] != "0" && fields[4] == "false" && strings.HasPrefix(condition, "False")
	})
	t.Logf("%s: pg_stat_archiver %q; Cluster ContinuousArchiving=%s", project, status, condition)
}

func stringOf(value interface{}) string {
	text, _ := value.(string)
	return text
}

// purgePrefixes removes what the run wrote to a shared bucket.
func (lab *backupLab) purgePrefixes(t *testing.T) {
	if r2StoreFromEnv() == nil || lab.store == nil {
		return
	}
	deleter, err := AWSObjectDeleterFactory(true)(context.Background(), lab.store)
	if err != nil {
		t.Logf("purge: %v", err)
		return
	}
	for _, project := range []string{lab.sourceID, "bkpbyid" + lab.suffix, "bkppitr" + lab.suffix} {
		n, err := deleteAllUnderPrefix(context.Background(), deleter, lab.store.Bucket, k8s.BarmanObjectPrefix(project), maxDeleteBatch)
		t.Logf("purged %d objects under %s (%v)", n, k8s.BarmanObjectPrefix(project), err)
	}
}
