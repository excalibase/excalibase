package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// K8sMajorUpgrader moves a CNPG cluster to a newer major the way CNPG 1.26+
// does it: change the image, and the operator stops every instance and runs
// pg_upgrade --link in a Job on the primary's volume; replicas are cloned
// again. The same update points the WAL archiver at the new major's own
// archive, because pg_upgrade starts a new system at timeline 1.
//
// Put back: only when the Job failed, or never appeared. Reverting the image
// deletes a running Job, so one that is merely slow is left alone.
type K8sMajorUpgrader struct {
	client   k8s.KubeClient
	backups  safetyBackupTaker
	archives *MajorUpgradeArchives
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error
	timings  K8sUpgradeTimings
}

// safetyBackupTaker takes a backup and returns once it completed.
type safetyBackupTaker interface {
	TakeSafetyBackup(ctx context.Context, inst *domain.DatabaseInstance) (string, error)
}

// K8sUpgradeTimings bounds each wait; zero values take the defaults.
type K8sUpgradeTimings struct {
	Poll        time.Duration // between looks at the cluster
	JobAppear   time.Duration // the operator starts the Job within this
	JobMax      time.Duration // a Job still running after this is left to an operator
	PutBack     time.Duration // the old major serves again within this
	AllServing  time.Duration // every instance serves within this after the conversion
	ServingPoll time.Duration
}

func (t K8sUpgradeTimings) withDefaults() K8sUpgradeTimings {
	defaults := K8sUpgradeTimings{Poll: 10 * time.Second, JobAppear: 15 * time.Minute, JobMax: 3 * time.Hour,
		PutBack: 15 * time.Minute, AllServing: 4 * time.Hour, ServingPoll: 30 * time.Second}
	for _, field := range []struct{ value, fallback *time.Duration }{
		{&t.Poll, &defaults.Poll}, {&t.JobAppear, &defaults.JobAppear}, {&t.JobMax, &defaults.JobMax},
		{&t.PutBack, &defaults.PutBack}, {&t.AllServing, &defaults.AllServing}, {&t.ServingPoll, &defaults.ServingPoll},
	} {
		if *field.value <= 0 {
			*field.value = *field.fallback
		}
	}
	return t
}

type K8sMajorUpgraderConfig struct {
	Client   k8s.KubeClient
	Backups  safetyBackupTaker
	Archives *MajorUpgradeArchives
	Timings  K8sUpgradeTimings
	Now      func() time.Time
	Sleep    func(context.Context, time.Duration) error
}

func NewK8sMajorUpgrader(cfg K8sMajorUpgraderConfig) *K8sMajorUpgrader {
	u := &K8sMajorUpgrader{client: cfg.Client, backups: cfg.Backups, archives: cfg.Archives,
		now: cfg.Now, sleep: cfg.Sleep, timings: cfg.Timings.withDefaults()}
	if u.now == nil {
		u.now = time.Now
	}
	if u.sleep == nil {
		u.sleep = sleepCtx
	}
	return u
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

const (
	cnpgPostgresContainer = "postgres"
	noArchiveBlocker      = "this database has no backup archive; turn on backups for this project"
	stillRunningPublic    = "the conversion is taking much longer than expected; it was left running and is checked again every few minutes, and the database is offline until it ends"
	notPutBackPublic      = "the upgrade failed and the database could not be started again on its old version"
	notRecordedPublic     = "the database was converted but the platform could not record it"
)

// clusterUpgrade is what one run changes on the cluster, and back.
type clusterUpgrade struct {
	namespace, name              string
	fromImage, toImage           string
	fromStore, fromServer        string
	toArchive, toStore, toServer string
	fromMajor, toMajor           int64
	projectID                    string
}

func (u *K8sMajorUpgrader) plan(ctx context.Context, inst *domain.DatabaseInstance, run domain.MajorUpgrade) (clusterUpgrade, error) {
	fromImage, err := config.PostgresImage(run.FromVersion)
	if err != nil {
		return clusterUpgrade{}, err
	}
	toImage, err := config.PostgresImage(run.ToVersion)
	if err != nil {
		return clusterUpgrade{}, err
	}
	fromArchive, err := u.archives.Archive(ctx, inst, run.FromVersion)
	if err != nil {
		return clusterUpgrade{}, err
	}
	toArchive, err := u.archives.Archive(ctx, inst, run.ToVersion)
	if err != nil {
		return clusterUpgrade{}, err
	}
	var fromMajor, toMajor int64
	if _, err := fmt.Sscan(run.FromVersion, &fromMajor); err != nil {
		return clusterUpgrade{}, fmt.Errorf("major %q: %w", run.FromVersion, err)
	}
	if _, err := fmt.Sscan(run.ToVersion, &toMajor); err != nil {
		return clusterUpgrade{}, fmt.Errorf("major %q: %w", run.ToVersion, err)
	}
	return clusterUpgrade{
		namespace: inst.Namespace, name: inst.ProjectID + postgresClusterSuffix, projectID: inst.ProjectID,
		fromImage: fromImage, toImage: toImage,
		fromStore: k8s.ArchiveObjectStoreName(inst.ProjectID, fromArchive), fromServer: k8s.ArchiveServerName(fromArchive),
		toArchive: toArchive, toStore: k8s.ArchiveObjectStoreName(inst.ProjectID, toArchive), toServer: k8s.ArchiveServerName(toArchive),
		fromMajor: fromMajor, toMajor: toMajor,
	}, nil
}

func (u *K8sMajorUpgrader) getCluster(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	cluster, err := u.client.GetCRD(ctx, k8s.CNPGClusterGVR, namespace, name)
	if err != nil {
		return nil, err
	}
	return cluster.DeepCopy(), nil
}

// Preflight runs the database checks on the primary. See MajorUpgrader.
func (u *K8sMajorUpgrader) Preflight(ctx context.Context, inst *domain.DatabaseInstance, target string) ([]string, error) {
	cluster, err := u.getCluster(ctx, inst.Namespace, inst.ProjectID+postgresClusterSuffix)
	if err != nil {
		return nil, fmt.Errorf("%w: read cluster: %v", ErrMajorUpgradeCheckUnavailable, err)
	}
	if _, _, ok := k8s.ClusterArchive(cluster); !ok {
		return []string{noArchiveBlocker}, nil
	}
	primary, _, _ := unstructured.NestedString(cluster.Object, "status", "currentPrimary")
	if primary == "" {
		return nil, fmt.Errorf("%w: the cluster has no primary", ErrMajorUpgradeCheckUnavailable)
	}
	out, err := u.client.ExecInPodStdin(ctx, inst.Namespace, primary, cnpgPostgresContainer, []string{"sh", "-s"}, preflightScript())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMajorUpgradeCheckUnavailable, err)
	}
	findings, err := parsePreflight(out)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMajorUpgradeCheckUnavailable, err)
	}
	return preflightBlockers(findings, target, nil), nil
}

// TakeBackup takes a CNPG backup and waits for it. See MajorUpgrader.
func (u *K8sMajorUpgrader) TakeBackup(ctx context.Context, inst *domain.DatabaseInstance) (string, error) {
	return u.backups.TakeSafetyBackup(ctx, inst)
}

// Convert points the cluster at the new major and watches the operator
// convert it. See MajorUpgrader.
func (u *K8sMajorUpgrader) Convert(ctx context.Context, inst *domain.DatabaseInstance, run domain.MajorUpgrade, progress UpgradeProgress) UpgradeOutcome {
	progress.Step(domain.MajorUpgradeStepConverting)
	plan, err := u.plan(ctx, inst, run)
	if err != nil {
		return nothingChanged(run, err)
	}
	if err := u.ensureArchiveStore(ctx, plan); err != nil {
		return nothingChanged(run, err)
	}
	started := u.now()
	if err := u.pointCluster(ctx, plan, plan.toImage, plan.toStore, plan.toServer); err != nil {
		image, known := u.imageAfterFailedWrite(ctx, plan)
		if known && image != plan.toImage {
			return nothingChanged(run, err)
		}
		// Applied, or unknown: the watch puts the old major back if no
		// conversion starts, which is harmless if the write never landed.
		log.Printf("major upgrade %s: point cluster: %v; watching in case it applied", plan.projectID, err)
	}
	progress.WritesStopped(started)
	return u.watch(ctx, run, plan, progress, started)
}

func nothingChanged(run domain.MajorUpgrade, cause error) UpgradeOutcome {
	return UpgradeOutcome{RolledBack: true, Cause: cause,
		Public: fmt.Sprintf("the upgrade could not start, so nothing was changed; the database is still on Postgres %s", run.FromVersion)}
}

func specImage(cluster *unstructured.Unstructured) string {
	image, _, _ := unstructured.NestedString(cluster.Object, "spec", "imageName")
	return image
}

// ensureArchiveStore creates the new major's ObjectStore as a copy of the
// one the cluster archives to now. An existing one is left as it is.
func (u *K8sMajorUpgrader) ensureArchiveStore(ctx context.Context, plan clusterUpgrade) error {
	cluster, err := u.getCluster(ctx, plan.namespace, plan.name)
	if err != nil {
		return fmt.Errorf("read cluster: %w", err)
	}
	if _, _, ok := k8s.ClusterArchive(cluster); !ok {
		return k8s.ErrClusterHasNoArchive
	}
	if exists, err := u.client.CRDExists(ctx, k8s.ObjectStoreGVR, plan.namespace, plan.toStore); err == nil && exists {
		return nil
	}
	current, err := u.client.GetCRD(ctx, k8s.ObjectStoreGVR, plan.namespace, plan.fromStore)
	if err != nil {
		return fmt.Errorf("read backup store %s: %w", plan.fromStore, err)
	}
	next, err := k8s.BuildArchiveObjectStore(current.DeepCopy(), plan.projectID, plan.toArchive)
	if err != nil {
		return err
	}
	return u.client.ApplyCRD(ctx, k8s.ObjectStoreGVR, plan.namespace, next)
}

// pointCluster sets image and archive in one update, again on a conflict.
func (u *K8sMajorUpgrader) pointCluster(ctx context.Context, plan clusterUpgrade, image, store, server string) error {
	var err error
	for attempt := 0; attempt < clusterUpdateAttempts; attempt++ {
		var cluster *unstructured.Unstructured
		if cluster, err = u.getCluster(ctx, plan.namespace, plan.name); err != nil {
			return err
		}
		if err = k8s.SetClusterArchive(cluster, image, store, server); err != nil {
			return err
		}
		if err = u.client.UpdateCRD(ctx, k8s.CNPGClusterGVR, plan.namespace, cluster); !apierrors.IsConflict(err) {
			return err
		}
	}
	return err
}

// clusterReadAttempts bounds the reads that settle whether a failed write
// landed.
const clusterReadAttempts = 5

// imageAfterFailedWrite reads the cluster's image after an update that
// failed; known is false when the cluster could not be read.
func (u *K8sMajorUpgrader) imageAfterFailedWrite(ctx context.Context, plan clusterUpgrade) (image string, known bool) {
	for attempt := 0; attempt < clusterReadAttempts; attempt++ {
		if u.sleep(ctx, u.timings.Poll) != nil {
			return "", false
		}
		if cluster, err := u.getCluster(ctx, plan.namespace, plan.name); err == nil {
			return specImage(cluster), true
		}
	}
	return "", false
}

// unsettled leaves a conversion that may still finish for recovery to check.
func unsettled(cause error) UpgradeOutcome {
	return UpgradeOutcome{Unsettled: true, Public: stillRunningPublic, Cause: cause}
}

// watch waits for the operator to convert the data, putting the old major
// back when the conversion failed or never started. Past JobMax it stops
// watching without deciding: recovery checks the run again later.
func (u *K8sMajorUpgrader) watch(ctx context.Context, run domain.MajorUpgrade, plan clusterUpgrade, progress UpgradeProgress, started time.Time) UpgradeOutcome {
	sawJob := false
	for {
		if err := u.sleep(ctx, u.timings.Poll); err != nil {
			return unsettled(err)
		}
		elapsed := u.now().Sub(started)
		cluster, err := u.getCluster(ctx, plan.namespace, plan.name)
		if err != nil {
			log.Printf("major upgrade %s: read cluster: %v", plan.projectID, err)
			if elapsed >= u.timings.JobMax {
				return unsettled(fmt.Errorf("cluster unreadable after %s: %w", elapsed, err))
			}
			continue
		}
		if major, ok := k8s.ClusterDataMajor(cluster); ok && major == plan.toMajor {
			if err := progress.Converted(u.now()); err != nil {
				return UpgradeOutcome{Public: notRecordedPublic, Cause: err}
			}
			return UpgradeOutcome{Converted: true}
		}
		state := u.jobState(ctx, plan, started)
		sawJob = sawJob || state != k8s.UpgradeJobAbsent
		switch {
		case state == k8s.UpgradeJobFailed:
			return u.putBack(ctx, run, plan, errors.New("the pg_upgrade job failed"))
		case !sawJob && elapsed >= u.timings.JobAppear:
			return u.putBack(ctx, run, plan, errors.New("the operator never started the pg_upgrade job"))
		case elapsed >= u.timings.JobMax:
			return unsettled(fmt.Errorf("pg_upgrade still running after %s", elapsed))
		}
	}
}

// jobClockSkew allows for the API server's clock running behind ours.
const jobClockSkew = time.Minute

func (u *K8sMajorUpgrader) jobState(ctx context.Context, plan clusterUpgrade, started time.Time) k8s.UpgradeJobState {
	jobs, err := u.client.ListCRDs(ctx, k8s.JobGVR, plan.namespace)
	if err != nil {
		log.Printf("major upgrade %s: list jobs: %v", plan.projectID, err)
		return k8s.UpgradeJobRunning // unknown: neither put back nor counted as absent
	}
	return k8s.MajorUpgradeJobState(jobs, plan.name, started.Add(-jobClockSkew))
}

// putBack points the cluster at the old image and archive again and waits
// for it to serve on the old major.
func (u *K8sMajorUpgrader) putBack(ctx context.Context, run domain.MajorUpgrade, plan clusterUpgrade, why error) UpgradeOutcome {
	log.Printf("major upgrade %s: putting Postgres %s back: %v", plan.projectID, run.FromVersion, why)
	if err := u.pointCluster(ctx, plan, plan.fromImage, plan.fromStore, plan.fromServer); err != nil {
		return UpgradeOutcome{Public: notPutBackPublic, Cause: fmt.Errorf("%v; put back: %w", why, err)}
	}
	deadline := u.now().Add(u.timings.PutBack)
	for u.now().Before(deadline) {
		if err := u.sleep(ctx, u.timings.Poll); err != nil {
			break
		}
		cluster, err := u.getCluster(ctx, plan.namespace, plan.name)
		if err != nil {
			continue
		}
		if major, ok := k8s.ClusterDataMajor(cluster); ok && major == plan.fromMajor && k8s.ClusterPrimaryServing(cluster) {
			return UpgradeOutcome{RolledBack: true, Cause: why}
		}
	}
	return UpgradeOutcome{Public: notPutBackPublic, Cause: fmt.Errorf("%v; the old major did not serve within %s", why, u.timings.PutBack)}
}

// Recover ends a run whose driver stopped before recording the conversion.
// See MajorUpgrader.
func (u *K8sMajorUpgrader) Recover(ctx context.Context, inst *domain.DatabaseInstance, run domain.MajorUpgrade, progress UpgradeProgress) UpgradeOutcome {
	plan, err := u.plan(ctx, inst, run)
	if err != nil {
		return UpgradeOutcome{Public: notPutBackPublic, Cause: err}
	}
	cluster, err := u.getCluster(ctx, plan.namespace, plan.name)
	if err != nil {
		return UpgradeOutcome{Public: notPutBackPublic, Cause: err}
	}
	if major, ok := k8s.ClusterDataMajor(cluster); ok && major == plan.toMajor {
		if err := progress.Converted(u.now()); err != nil {
			return UpgradeOutcome{Public: notRecordedPublic, Cause: err}
		}
		return UpgradeOutcome{Converted: true}
	}
	if specImage(cluster) != plan.toImage {
		return nothingChanged(run, errors.New("the cluster was never pointed at the new major"))
	}
	started := u.now()
	if run.WritesStoppedAt != nil {
		started = *run.WritesStoppedAt
	} else {
		progress.WritesStopped(started)
	}
	progress.Step(domain.MajorUpgradeStepConverting)
	return u.watch(ctx, run, plan, progress, started)
}

// WaitServing waits for every instance to serve; replicas are cloned again
// after a conversion. See MajorUpgrader.
func (u *K8sMajorUpgrader) WaitServing(ctx context.Context, inst *domain.DatabaseInstance) error {
	name := inst.ProjectID + postgresClusterSuffix
	deadline := u.now().Add(u.timings.AllServing)
	for {
		cluster, err := u.getCluster(ctx, inst.Namespace, name)
		if err == nil && k8s.ClusterFullyServing(cluster) {
			return nil
		}
		if !u.now().Before(deadline) {
			return fmt.Errorf("cluster %s not fully serving within %s", name, u.timings.AllServing)
		}
		if err := u.sleep(ctx, u.timings.ServingPoll); err != nil {
			return err
		}
	}
}

// Cleanup has nothing to remove: the old archive's store is kept for
// restores of earlier points. See MajorUpgrader.
func (u *K8sMajorUpgrader) Cleanup(context.Context, *domain.DatabaseInstance, domain.MajorUpgrade) {}

// afterUpgradeScript updates every extension in every database, then builds
// planner statistics, which pg_upgrade does not carry, in stages.
const afterUpgradeScript = `set -u
export PGOPTIONS='` + superuserSessionOptions + ` -c statement_timeout=0'
psql -X -A -t -q -v ON_ERROR_STOP=1 -U postgres -d postgres \
  -c "SELECT datname FROM pg_catalog.pg_database WHERE datallowconn AND NOT datistemplate" |
while IFS= read -r db; do
  printf '%s\n' "SELECT pg_catalog.format('ALTER EXTENSION %I UPDATE', extname) FROM pg_catalog.pg_extension" '\gexec' |
    psql -X -q -U postgres -d "$db" || echo "extension update failed in a database" >&2
done
vacuumdb --all --analyze-in-stages -U postgres
`

const afterStepsWarning = "statistics or extensions could not be refreshed after the upgrade; queries may be slow until ANALYZE runs"

// AfterSteps runs afterUpgradeScript on the primary. See MajorUpgrader.
func (u *K8sMajorUpgrader) AfterSteps(ctx context.Context, inst *domain.DatabaseInstance) []string {
	cluster, err := u.getCluster(ctx, inst.Namespace, inst.ProjectID+postgresClusterSuffix)
	if err != nil {
		log.Printf("major upgrade %s: after steps: %v", inst.ProjectID, err)
		return []string{afterStepsWarning}
	}
	primary, _, _ := unstructured.NestedString(cluster.Object, "status", "currentPrimary")
	if _, err := u.client.ExecInPodStdin(ctx, inst.Namespace, primary, cnpgPostgresContainer, []string{"sh", "-s"}, afterUpgradeScript); err != nil {
		log.Printf("major upgrade %s: after steps: %v", inst.ProjectID, err)
		return []string{afterStepsWarning}
	}
	return nil
}
