package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var (
	// ErrRestoreTargetInFuture refuses a point-in-time target the source has
	// not reached: there is nothing to recover to yet.
	ErrRestoreTargetInFuture = errors.New("restore refused: targetTime is later than the source database's current time")
	// ErrRestoreTargetNotArchived refuses a target whose WAL the source has
	// not archived yet, so a recovery would end before it.
	ErrRestoreTargetNotArchived = errors.New("restore refused: the source's WAL up to targetTime is not archived yet; nothing was created, retry once WAL archiving catches up")
	// ErrRestoreTargetNotReached fails a restore whose recovery ran out of
	// archived WAL before the target: Postgres refuses to promote it.
	ErrRestoreTargetNotReached = errors.New("restore failed: recovery ended before targetTime was reached; the archived WAL does not cover it")
	// ErrRestoreTargetGuardNotConfigured refuses a point-in-time restore on a
	// platform that cannot prove its target recoverable.
	ErrRestoreTargetGuardNotConfigured = errors.New("restore: point-in-time target check not configured")
)

const (
	defaultArchivePoll    = 2 * time.Second
	defaultArchiveTimeout = 2 * time.Minute
	// walSegmentNameLength is a WAL segment's file name: timeline, log, segment.
	walSegmentNameLength = 24
	hibernationOn        = "on"
	recoveryLogTailLines = 200
)

// RestoreTargetGuard proves a point-in-time target recoverable before a
// restore creates anything.
type RestoreTargetGuard interface {
	EnsureRecoverable(ctx context.Context, src *domain.DatabaseInstance, target time.Time) error
}

// ArchivedWALGuard makes a running source archive everything up to the target:
// a commit after the target (recovery only knows it reached a time target
// when it replays a later commit), a WAL switch, then a bounded wait until
// pg_stat_archiver has archived that segment.
type ArchivedWALGuard struct {
	k8sClient k8s.KubeClient
	poller    provisioner.Poller
	now       func() time.Time
}

func NewArchivedWALGuard(client k8s.KubeClient) *ArchivedWALGuard {
	return &ArchivedWALGuard{
		k8sClient: client,
		poller:    provisioner.NewPoller(defaultArchivePoll, defaultArchiveTimeout),
		now:       time.Now,
	}
}

func (g *ArchivedWALGuard) EnsureRecoverable(ctx context.Context, src *domain.DatabaseInstance, target time.Time) error {
	primary, err := g.servingPrimary(ctx, src)
	if err != nil {
		return err
	}
	if primary == "" {
		// Nothing more can be archived from a stopped source. Recovery is the
		// judge: Postgres fails it if the archive ends before the target.
		if target.After(g.now()) {
			return ErrRestoreTargetInFuture
		}
		log.Printf("restore %s: source not serving; recovery must reach %s on the WAL already archived", src.ProjectID, target.UTC().Format(time.RFC3339Nano))
		return nil
	}
	segment, err := g.closeSegmentPast(ctx, src.Namespace, primary, target)
	if err != nil {
		return err
	}
	return g.waitArchived(ctx, src, primary, segment)
}

// servingPrimary is the source's primary pod when it is serving, "" when the
// source is hibernated, has no primary or the primary is not ready.
func (g *ArchivedWALGuard) servingPrimary(ctx context.Context, src *domain.DatabaseInstance) (string, error) {
	cluster, err := g.k8sClient.GetCRD(ctx, k8s.CNPGClusterGVR, src.Namespace, src.ProjectID+postgresClusterSuffix)
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("restore %s: read source cluster: %w", src.ProjectID, err)
	}
	if cluster.GetAnnotations()["cnpg.io/hibernation"] == hibernationOn {
		return "", nil
	}
	primary, _, _ := unstructured.NestedString(cluster.Object, "status", "currentPrimary")
	if primary == "" {
		return "", nil
	}
	if ready, err := g.k8sClient.IsPodReady(ctx, src.Namespace, primary); err != nil || !ready {
		return "", nil
	}
	return primary, nil
}

// closeSegmentPast refuses a target the source's clock has not passed, then
// commits after it and switches WAL. It returns the segment that must be
// archived for recovery to reach the target. Each -c is its own transaction.
func (g *ArchivedWALGuard) closeSegmentPast(ctx context.Context, namespace, primary string, target time.Time) (string, error) {
	at := domain.ZonedTime{Time: target}.RecoveryTime()
	passed, err := g.psql(ctx, namespace, primary,
		fmt.Sprintf("SELECT clock_timestamp() > '%s'::timestamptz", at))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(passed) != "t" {
		return "", ErrRestoreTargetInFuture
	}
	out, err := g.psql(ctx, namespace, primary, "SELECT txid_current()", "SELECT pg_walfile_name(pg_switch_wal())")
	if err != nil {
		return "", err
	}
	lines := strings.Fields(out)
	if len(lines) != 2 || len(lines[1]) != walSegmentNameLength {
		return "", fmt.Errorf("restore: unexpected WAL switch answer %q", out)
	}
	return lines[1], nil
}

func (g *ArchivedWALGuard) waitArchived(ctx context.Context, src *domain.DatabaseInstance, primary, segment string) error {
	var last string
	err := g.poller.WaitUntilReady(ctx, "WAL segment "+segment+" archived", func(ctx context.Context) (bool, error) {
		out, err := g.psql(ctx, src.Namespace, primary, "SELECT coalesce(last_archived_wal, '') FROM pg_stat_archiver")
		if err != nil {
			return false, err
		}
		last = strings.TrimSpace(out)
		return isSegmentAtOrAfter(last, segment), nil
	})
	if errors.Is(err, provisioner.ErrWaitTimeout) {
		log.Printf("restore %s: segment %s not archived within %s (last archived %q)", src.ProjectID, segment, g.poller.Timeout, last)
		return ErrRestoreTargetNotArchived
	}
	return err
}

// isSegmentAtOrAfter compares WAL segment names; a history or backup-label
// file is not a segment and proves nothing.
func isSegmentAtOrAfter(archived, segment string) bool {
	if len(archived) < walSegmentNameLength || strings.Contains(archived[:walSegmentNameLength], ".") {
		return false
	}
	return archived[:walSegmentNameLength] >= segment
}

func (g *ArchivedWALGuard) psql(ctx context.Context, namespace, pod string, statements ...string) (string, error) {
	cmd := []string{"psql", "-U", "postgres", "-v", "ON_ERROR_STOP=1", "-tAX"}
	for _, statement := range statements {
		cmd = append(cmd, "-c", statement)
	}
	out, err := g.k8sClient.ExecInPod(ctx, namespace, pod, "postgres", cmd)
	if err != nil {
		return "", fmt.Errorf("restore: query source primary %s: %w", pod, err)
	}
	return out, nil
}

// recoveryNotReachedMessage is what Postgres logs (then exits) when recovery
// runs out of WAL before its recovery target.
const recoveryNotReachedMessage = "recovery ended before configured recovery target was reached"

// recoveryMissedTarget reports ErrRestoreTargetNotReached once a recovery pod
// failed with Postgres's own verdict. Other failures are the Job's to retry.
func (a *K8sBackupAdapter) recoveryMissedTarget(ctx context.Context, namespace, cluster string) error {
	pods, err := a.k8sClient.GetPods(ctx, namespace, "cnpg.io/jobRole=full-recovery,cnpg.io/cluster="+cluster)
	if err != nil {
		log.Printf("restore %s: list recovery pods: %v", cluster, err)
		return nil
	}
	for _, pod := range pods {
		if pod.Status.Phase != corev1.PodFailed {
			continue
		}
		tail, err := a.k8sClient.PodLogTail(ctx, namespace, pod.Name, "full-recovery", recoveryLogTailLines)
		if err != nil {
			log.Printf("restore %s: read recovery pod %s log: %v", cluster, pod.Name, err)
			continue
		}
		if strings.Contains(tail, recoveryNotReachedMessage) {
			return ErrRestoreTargetNotReached
		}
	}
	return nil
}
