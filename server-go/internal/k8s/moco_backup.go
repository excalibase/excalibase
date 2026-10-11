package k8s

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// MySQL backups run through MOCO (ADR 0041 decision 6): a BackupPolicy per
// project drives a CronJob that dumps the cluster plus the binlogs since the
// previous run into the project's own prefix of the platform bucket.
var (
	MOCOBackupPolicyGVR = schema.GroupVersionResource{Group: "moco.cybozu.com", Version: "v1beta2", Resource: "backuppolicies"}
	ServiceAccountGVR   = schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}
	JobGVR              = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
)

const (
	// MySQLBackupServiceAccount runs the backup and restore jobs; MOCO binds
	// the role they need to it.
	MySQLBackupServiceAccount = "mysql-backup"
	// MySQLBackupAccessName is the Secret with the project's read-write
	// object-store access to its own prefix, as the AWS variables the job reads.
	MySQLBackupAccessName = "mysql-backup-creds"
	// MySQLRestoreSourceAccessName is the Secret with a restore's read-only
	// access to the source's prefix.
	MySQLRestoreSourceAccessName = "mysql-restore-source-creds"

	mocoBackupKeyRoot      = "moco"
	mocoBackupJobPrefix    = "moco-backup-"
	mocoRestoreJobPrefix   = "moco-restore-"
	manualBackupAnnotation = "cronjob.kubernetes.io/instantiate"
	manualBackupInfix      = "-manual-"
	// MOCOBackupTimeFormat names each backup's directory (pkg/constants).
	MOCOBackupTimeFormat = "20060102-150405"
	// MOCODumpFile and MOCOBinlogFile are what a backup directory holds.
	MOCODumpFile   = "dump.tar"
	MOCOBinlogFile = "binlog.tar.zst"
)

// MySQL job phases as the platform reads them.
const (
	MySQLJobRunning   = "running"
	MySQLJobSucceeded = "succeeded"
	MySQLJobFailed    = "failed"
)

// MOCOBackupPrefix is where MOCO writes a project's backups:
// moco/<namespace>/<cluster>/ (backup/key.go calcPrefix, MOCO v0.37.0).
func MOCOBackupPrefix(namespace, projectID string) string {
	return path.Join(mocoBackupKeyRoot, namespace, MySQLClusterName(projectID)) + "/"
}

// MySQLBackupPolicyName is the project's BackupPolicy.
func MySQLBackupPolicyName(projectID string) string { return MySQLClusterName(projectID) + "-backup" }

// MySQLBackupCronJobName is the CronJob MOCO runs the policy with.
func MySQLBackupCronJobName(projectID string) string {
	return mocoBackupJobPrefix + MySQLClusterName(projectID)
}

// MySQLRestoreJobName is the Job MOCO restores a cluster with.
func MySQLRestoreJobName(projectID string) string {
	return mocoRestoreJobPrefix + MySQLClusterName(projectID)
}

// MySQLBackupStore is the bucket the jobs read and write.
type MySQLBackupStore struct {
	Bucket    string
	Endpoint  string
	Region    string
	PathStyle bool
}

// MySQLBackupOpts is what a project's BackupPolicy is built from.
type MySQLBackupOpts struct {
	ProjectID string
	Namespace string
	// Schedule is a five-field cron schedule.
	Schedule string
	Plan     config.TierConfig
	// Disk sizes the job's work volume: a full dump is staged there.
	Disk         string
	StorageClass string
	Store        MySQLBackupStore
}

func validateBackupOpts(opts MySQLBackupOpts) error {
	switch {
	case opts.ProjectID == "" || opts.Namespace == "":
		return errors.New("a MySQL backup policy needs its project and namespace")
	case opts.Store.Bucket == "" || opts.Store.Endpoint == "":
		return errors.New("a MySQL backup policy needs the store's bucket and endpoint")
	case opts.Disk == "":
		return errors.New("a MySQL backup policy needs the disk its dump is staged on")
	}
	return ValidateBackupSchedule(opts.Schedule)
}

// BuildMySQLBackupPolicy renders the project's BackupPolicy: never two runs
// at once, one finished job of each kind kept, sized by plan.
func BuildMySQLBackupPolicy(opts MySQLBackupOpts) (*unstructured.Unstructured, error) {
	if err := validateBackupOpts(opts); err != nil {
		return nil, err
	}
	job, err := mysqlJobConfig(opts, MySQLBackupAccessName)
	if err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": mocoAPIVersion,
		"kind":       "BackupPolicy",
		"metadata": map[string]interface{}{
			"name":      MySQLBackupPolicyName(opts.ProjectID),
			"namespace": opts.Namespace,
			"labels":    map[string]interface{}{"excalibase.io/project": opts.ProjectID},
		},
		"spec": map[string]interface{}{
			"schedule":                   opts.Schedule,
			"concurrencyPolicy":          "Forbid",
			"successfulJobsHistoryLimit": int64(1),
			"failedJobsHistoryLimit":     int64(1),
			"backoffLimit":               int64(1),
			"jobConfig":                  job,
		},
	}}, nil
}

// mysqlJobConfig is the shared shape of the backup and restore jobs. Every
// bound is explicit: MOCO's own default is 4 CPUs and 4Gi.
func mysqlJobConfig(opts MySQLBackupOpts, credentialsSecret string) (map[string]interface{}, error) {
	plan, err := config.MySQLPlan(opts.Plan)
	if err != nil {
		return nil, err
	}
	small, err := smallMySQLPlan(plan)
	if err != nil {
		return nil, err
	}
	sizing := map[string]interface{}{"threads": int64(2), "cpu": "500m", "maxCpu": "2", "memory": "1Gi", "maxMemory": "2Gi"}
	if small {
		sizing = map[string]interface{}{"threads": int64(1), "cpu": "100m", "maxCpu": "500m", "memory": "256Mi", "maxMemory": "1Gi"}
	}
	bucket := map[string]interface{}{
		"bucketName":   opts.Store.Bucket,
		"endpointURL":  opts.Store.Endpoint,
		"usePathStyle": opts.Store.PathStyle,
		"backendType":  "s3",
	}
	if opts.Store.Region != "" {
		bucket["region"] = opts.Store.Region
	}
	job := map[string]interface{}{
		"serviceAccountName": MySQLBackupServiceAccount,
		"bucketConfig":       bucket,
		"envFrom":            []interface{}{map[string]interface{}{"secretRef": map[string]interface{}{"name": credentialsSecret}}},
		"workVolume":         mysqlWorkVolume(opts.Disk, opts.StorageClass),
	}
	for key, value := range sizing {
		job[key] = value
	}
	return job, nil
}

// mysqlWorkVolume is a generic ephemeral volume the size of the database
// disk, released with the job's pod.
func mysqlWorkVolume(disk, storageClass string) map[string]interface{} {
	claim := map[string]interface{}{
		"accessModes": []interface{}{"ReadWriteOnce"},
		"resources":   map[string]interface{}{"requests": map[string]interface{}{"storage": disk}},
	}
	if storageClass != "" {
		claim["storageClassName"] = storageClass
	}
	return map[string]interface{}{"ephemeral": map[string]interface{}{"volumeClaimTemplate": map[string]interface{}{"spec": claim}}}
}

// BuildMySQLBackupServiceAccount is the account the jobs run as.
func BuildMySQLBackupServiceAccount(namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ServiceAccount",
		"metadata":   map[string]interface{}{"name": MySQLBackupServiceAccount, "namespace": namespace},
	}}
}

// MySQLBackupAccessNameData is the Secret content for a temporary
// credential, as the AWS SDK's environment chain reads it, with the metadata
// renewal reads.
func MySQLBackupAccessNameData(creds *domain.S3Credentials) (map[string][]byte, error) {
	if creds == nil || creds.SessionToken == "" || creds.ExpiresAt.IsZero() {
		return nil, ErrLongLivedBackupKey
	}
	data := map[string][]byte{
		"AWS_ACCESS_KEY_ID":           []byte(creds.AccessKeyID),
		"AWS_SECRET_ACCESS_KEY":       []byte(creds.SecretAccessKey),
		"AWS_SESSION_TOKEN":           []byte(creds.SessionToken),
		BackupCredentialsExpiresAtKey: []byte(creds.ExpiresAt.UTC().Format(time.RFC3339)),
		BackupCredentialsIssuedByKey:  []byte(creds.IssuedBy),
		BackupCredentialsBucketKey:    []byte(creds.Bucket),
		BackupCredentialsEndpointKey:  []byte(creds.Endpoint),
	}
	if creds.Region != "" {
		data["AWS_REGION"] = []byte(creds.Region)
	}
	return data, nil
}

// WithMySQLBackupPolicy is a copy of the cluster backed up by the project's policy.
func WithMySQLBackupPolicy(cluster *unstructured.Unstructured, projectID string) *unstructured.Unstructured {
	out := cluster.DeepCopy()
	_ = unstructured.SetNestedField(out.Object, MySQLBackupPolicyName(projectID), "spec", "backupPolicyName")
	return out
}

// WithoutMySQLBackupPolicy is a copy of the cluster with no backups.
func WithoutMySQLBackupPolicy(cluster *unstructured.Unstructured) *unstructured.Unstructured {
	out := cluster.DeepCopy()
	unstructured.RemoveNestedField(out.Object, "spec", "backupPolicyName")
	return out
}

// MySQLRestoreSource is the backup a new cluster is restored from.
type MySQLRestoreSource struct {
	Namespace string
	ProjectID string
	// Point is the restore point; MOCO reads it to the second.
	Point             time.Time
	CredentialsSecret string
}

// WithMySQLRestore is a copy of a not-yet-created cluster that MOCO fills
// from source. It carries no backup policy: backups start once the data is in.
func WithMySQLRestore(cluster *unstructured.Unstructured, source MySQLRestoreSource, job MySQLBackupOpts) (*unstructured.Unstructured, error) {
	if source.Namespace == "" || source.ProjectID == "" || source.Point.IsZero() || source.CredentialsSecret == "" {
		return nil, errors.New("a MySQL restore needs its source cluster, point and credentials")
	}
	if job.Store.Bucket == "" || job.Store.Endpoint == "" || job.Disk == "" {
		return nil, errors.New("a MySQL restore needs the store's bucket and endpoint and a work disk")
	}
	jobConfig, err := mysqlJobConfig(job, source.CredentialsSecret)
	if err != nil {
		return nil, err
	}
	out := WithoutMySQLBackupPolicy(cluster)
	restore := map[string]interface{}{
		"sourceName":      MySQLClusterName(source.ProjectID),
		"sourceNamespace": source.Namespace,
		"restorePoint":    source.Point.UTC().Format(time.RFC3339),
		"jobConfig":       jobConfig,
	}
	if err := unstructured.SetNestedMap(out.Object, restore, "spec", "restore"); err != nil {
		return nil, err
	}
	return out, nil
}

// MySQLClusterRestored is MOCO reporting a restoring cluster's data in; a
// cluster that restores nothing is trivially so.
func MySQLClusterRestored(cluster *unstructured.Unstructured) bool {
	if _, found, _ := unstructured.NestedMap(cluster.Object, "spec", "restore"); !found {
		return true
	}
	restored, _, _ := unstructured.NestedString(cluster.Object, "status", "restoredTime")
	return restored != ""
}

// BuildManualMySQLBackupJob is a backup started now from the CronJob's
// template and owned by it, as `kubectl create job --from=cronjob` makes one.
func BuildManualMySQLBackupJob(cronJob *unstructured.Unstructured, at time.Time) (*unstructured.Unstructured, error) {
	template, found, err := unstructured.NestedMap(cronJob.Object, "spec", "jobTemplate")
	if err != nil || !found {
		return nil, fmt.Errorf("backup CronJob %s has no job template", cronJob.GetName())
	}
	spec, found, _ := unstructured.NestedMap(template, "spec")
	if !found {
		return nil, fmt.Errorf("backup CronJob %s has no job spec", cronJob.GetName())
	}
	labels, _, _ := unstructured.NestedStringMap(template, "metadata", "labels")
	job := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"spec":       spec,
	}}
	job.SetName(cronJob.GetName() + manualBackupInfix + at.UTC().Format("20060102150405"))
	job.SetNamespace(cronJob.GetNamespace())
	job.SetLabels(labels)
	job.SetAnnotations(map[string]string{manualBackupAnnotation: "manual"})
	controller := true
	job.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: "batch/v1", Kind: "CronJob", Name: cronJob.GetName(), UID: cronJob.GetUID(), Controller: &controller,
	}})
	return job, nil
}

// IsMySQLBackupJob reports whether job is one of the project's backup runs,
// scheduled or manual.
func IsMySQLBackupJob(job *unstructured.Unstructured, projectID string) bool {
	return strings.HasPrefix(job.GetName(), MySQLBackupCronJobName(projectID)+"-")
}

// IsManualMySQLBackupJob reports whether a backup run was started by hand.
func IsManualMySQLBackupJob(job *unstructured.Unstructured) bool {
	return job.GetAnnotations()[manualBackupAnnotation] == "manual"
}

// MySQLJobState is a Job's phase and when it ran.
type MySQLJobState struct {
	Phase    string
	Started  time.Time
	Finished time.Time
	Reason   string
}

// MySQLBackupJobState reads a backup or restore Job's phase from its status.
func MySQLBackupJobState(job *unstructured.Unstructured) MySQLJobState {
	state := MySQLJobState{Phase: MySQLJobRunning}
	state.Started = jobTime(job, "startTime")
	if state.Started.IsZero() {
		state.Started = job.GetCreationTimestamp().Time
	}
	conditions, _, _ := unstructured.NestedSlice(job.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, ok := raw.(map[string]interface{})
		if !ok || condition["status"] != "True" {
			continue
		}
		switch condition["type"] {
		case "Failed":
			state.Phase = MySQLJobFailed
			state.Reason, _ = condition["message"].(string)
		case "Complete":
			state.Phase = MySQLJobSucceeded
		}
	}
	if succeeded, _, _ := unstructured.NestedInt64(job.Object, "status", "succeeded"); succeeded > 0 {
		state.Phase = MySQLJobSucceeded
	}
	if state.Phase == MySQLJobSucceeded {
		state.Finished = jobTime(job, "completionTime")
	}
	return state
}

func jobTime(job *unstructured.Unstructured, field string) time.Time {
	raw, _, _ := unstructured.NestedString(job.Object, "status", field)
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// WithMySQLBackupSchedule is a copy of the policy on another schedule and
// plan sizing.
func WithMySQLBackupSchedule(policy *unstructured.Unstructured, schedule string, plan config.TierConfig) (*unstructured.Unstructured, error) {
	if err := ValidateBackupSchedule(schedule); err != nil {
		return nil, err
	}
	out := policy.DeepCopy()
	job, found, _ := unstructured.NestedMap(out.Object, "spec", "jobConfig")
	if !found {
		return nil, fmt.Errorf("backup policy %s has no job config", policy.GetName())
	}
	sized, err := mysqlJobConfig(MySQLBackupOpts{Plan: plan}, "")
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"threads", "cpu", "maxCpu", "memory", "maxMemory"} {
		job[key] = sized[key]
	}
	if err := unstructured.SetNestedMap(out.Object, job, "spec", "jobConfig"); err != nil {
		return nil, err
	}
	_ = unstructured.SetNestedField(out.Object, schedule, "spec", "schedule")
	return out, nil
}
