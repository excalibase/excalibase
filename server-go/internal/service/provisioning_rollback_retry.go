package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

const (
	// RollbackRetryInterval is how often a cleanup that failed is looked at, and
	// the first wait after a retry that failed.
	RollbackRetryInterval = 5 * time.Minute
	// rollbackRetryMaxDelay caps the wait between retries of one project.
	rollbackRetryMaxDelay = time.Hour
	// rollbackRetryAttemptLimit bounds what one attempt waits for under the
	// project's lease, so a stuck removal does not hold off DELETE or an add. The
	// next attempt carries on: every step is idempotent.
	rollbackRetryAttemptLimit = time.Minute
	// rollbackRetryParallelism is how many projects one pass works on at once, so
	// a stuck project does not hold up the rest.
	rollbackRetryParallelism = 4
	// rollbackRetrySlack keeps a retry due on the pass it is meant for, whatever
	// part of the attempt limit the failed attempt used.
	rollbackRetrySlack = 30 * time.Second

	// The failed add's reason says where its backup files stand. The notes are
	// the record the sweep works from, and rows already written carry them.
	purgeWaitingNote  = "; its backup files are removed once its database is"
	purgeFailedNote   = "; its backup files could not be removed, so a retry will be refused"
	purgeNoPurgerNote = "; its backup files were not removed (no backup purger), so a retry will be refused"
)

// The provisioners name these cleanups; a rollback log is matched against them.
func deleteNamespaceCleanup(namespace string) string { return "delete namespace " + namespace }

func removeDatabaseCleanup(namespace string) string {
	return "remove database from namespace " + namespace
}

func removeFailedBuildCleanup(projectID string) string { return "remove failed build " + projectID }

// leftover is what a failed rollback left for the sweep.
type leftover int

const (
	noLeftover leftover = iota
	leftoverNamespace
	leftoverSingleHostBuild
	leftoverAddedDatabase
	leftoverAddBackups
	leftoverRestore
	leftoverRestoreDeletion
)

// errRollbackInProgress is a retry that has nothing to do yet: no failure, so no
// backoff. The next pass looks again.
var errRollbackInProgress = errors.New("the rollback is waiting")

// leftoverOf reads what a row says was left behind. FAILED alone proves nothing,
// since a major upgrade can leave a live project FAILED with its data in the
// namespace, so the sweep touches a row only while it still reports the very
// failure whose rollback fell short, in its own namespace, and is not protected.
func (s *ProvisioningService) leftoverOf(inst *domain.DatabaseInstance) leftover {
	failed := inst.Status == string(domain.StageFailed)
	switch inst.DeploymentMode {
	case domain.ModeDocker:
		if failed && !isProtected(inst) && failedCleanupOf(inst, removeFailedBuildCleanup(inst.ProjectID)) {
			return leftoverSingleHostBuild
		}
	case domain.ModeK8s:
		return s.clusterLeftoverOf(inst)
	}
	return noLeftover
}

func (s *ProvisioningService) clusterLeftoverOf(inst *domain.DatabaseInstance) leftover {
	if s.k8sClient == nil {
		return noLeftover
	}
	switch {
	case inst.Status == string(domain.StageFailed):
		return failedProjectLeftover(inst)
	case inst.Status == string(domain.StatusDeleting) && isFailedRestore(inst):
		// The sweep's own final delete, stopped part way. Nobody asked for it, so
		// nobody else will retry it.
		return leftoverRestoreDeletion
	case inst.Status == string(domain.StatusActive) && inst.NoDatabase:
		return s.failedAddLeftover(inst)
	}
	return noLeftover
}

func failedProjectLeftover(inst *domain.DatabaseInstance) leftover {
	switch {
	case isProtected(inst):
		return noLeftover
	case isFailedRestore(inst):
		return leftoverRestore
	case ownsItsNamespace(inst) && failedCleanupOf(inst, deleteNamespaceCleanup(inst.Namespace)):
		return leftoverNamespace
	}
	return noLeftover
}

func (s *ProvisioningService) failedAddLeftover(inst *domain.DatabaseInstance) leftover {
	if failedCleanupOf(inst, removeDatabaseCleanup(inst.Namespace)) {
		return leftoverAddedDatabase
	}
	if s.backupPurger != nil && (strings.Contains(inst.FailureReason, purgeWaitingNote) ||
		strings.Contains(inst.FailureReason, purgeFailedNote) || strings.Contains(inst.FailureReason, purgeNoPurgerNote)) {
		return leftoverAddBackups
	}
	return noLeftover
}

// failedAddStillStands reports a failed add whose cluster has not been seen to go.
func failedAddStillStands(inst *domain.DatabaseInstance) bool {
	return failedCleanupOf(inst, removeDatabaseCleanup(inst.Namespace)) ||
		strings.Contains(inst.FailureReason, purgeWaitingNote)
}

// ownsItsNamespace is the name every project's namespace is created under.
func ownsItsNamespace(inst *domain.DatabaseInstance) bool {
	return inst.Namespace == fmt.Sprintf("%s-%s", inst.OrgID, inst.ProjectID)
}

// failedCleanupOf reports the named cleanup failing in the rollback of the failure
// the row still reports. A log kept after the row moved on licenses nothing. The
// failed add's reason can carry notes after the failure, hence the prefix.
func failedCleanupOf(inst *domain.DatabaseInstance, name string) bool {
	for _, result := range parseRollbackLog(inst.RollbackLog) {
		if result.Name == name && !result.OK && result.ForFailure != "" && strings.HasPrefix(inst.FailureReason, result.ForFailure) {
			return true
		}
	}
	return false
}

// isFailedRestore reports the row a restore left when it could not remove its
// copy. Each condition is something only that path writes together.
func isFailedRestore(inst *domain.DatabaseInstance) bool {
	if inst.RestoredFromProjectID == "" || inst.FailureReason != failedRestoreReason || !ownsItsNamespace(inst) {
		return false
	}
	for _, result := range parseRollbackLog(inst.RollbackLog) {
		if result.OK {
			continue
		}
		switch result.Name {
		case restoreNamespaceCleanup(inst.Namespace), restoreClusterCleanup(inst.Namespace), unverifiedRowCleanup:
			return true
		}
	}
	return false
}

// RetryFailedRollbacks finishes the rollbacks a failed cleanup left short and
// answers with the projects it finished. Projects run side by side, each backing
// off on its own while it keeps failing.
func (s *ProvisioningService) RetryFailedRollbacks(ctx context.Context) []string {
	all, err := s.store.FindAll()
	if err != nil {
		log.Printf("ERROR: list projects to retry failed rollbacks: %v", err)
		return nil
	}
	var (
		mu       sync.Mutex
		finished []string
		running  sync.WaitGroup
	)
	slots := make(chan struct{}, rollbackRetryParallelism)
	for _, inst := range all {
		if s.leftoverOf(inst) == noLeftover || !s.rollbackRetries.due(inst.ProjectID) {
			continue
		}
		projectID := inst.ProjectID
		slots <- struct{}{}
		running.Go(func() {
			defer func() { <-slots }()
			if s.retryProjectRollback(ctx, projectID) {
				mu.Lock()
				defer mu.Unlock()
				finished = append(finished, projectID)
			}
		})
	}
	running.Wait()
	return finished
}

// retryProjectRollback runs one project's attempt and schedules the next.
func (s *ProvisioningService) retryProjectRollback(ctx context.Context, projectID string) (done bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("ERROR: retry the failed rollback of %s panicked: %v\n%s", projectID, r, debug.Stack())
			s.rollbackRetries.failed(projectID)
			done = false
		}
	}()
	err := s.retryRollback(ctx, projectID)
	switch {
	case err == nil:
		s.rollbackRetries.succeeded(projectID)
		return true
	case errors.Is(err, ErrProjectOperationRunning), errors.Is(err, errRollbackInProgress):
		// Someone else's turn, or nothing to do yet; the next pass looks again.
		return false
	default:
		log.Printf("ERROR: retry the failed rollback of %s: %v", projectID, err)
		s.rollbackRetries.failed(projectID)
		return false
	}
}

// retryRollback judges the row again under the project's lease, which excludes
// a delete, a pause or a new add that started since the listing.
func (s *ProvisioningService) retryRollback(ctx context.Context, projectID string) error {
	teardown, err := s.retryUnderLease(ctx, projectID)
	if err != nil || !teardown {
		return err
	}
	// A delete started while the vault is sealed could only stop at its step and
	// leave the row in DELETING, so it waits for the vault to open.
	if s.vault != nil && s.vault.Sealed() {
		return errRollbackInProgress
	}
	// Takes the lease itself. The copy's namespace has been seen to go, so what
	// is left is the row and the rest of a normal delete.
	return s.DeprovisionWithOptions(ctx, projectID, DeprovisionOptions{})
}

func (s *ProvisioningService) retryUnderLease(ctx context.Context, projectID string) (teardown bool, err error) {
	inst, release, err := s.holdProject(ctx, projectID, OperationRollbackRetry, func(string, string) error { return nil })
	if err != nil {
		return false, err
	}
	defer release()
	attemptCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.rollbackRetries.limit())
	defer cancel()
	switch s.leftoverOf(inst) {
	case leftoverNamespace:
		return false, s.retryCleanup(inst, deleteNamespaceCleanup(inst.Namespace), s.k8sClient.DeleteNamespace(attemptCtx, inst.Namespace))
	case leftoverSingleHostBuild:
		return false, s.retrySingleHostBuild(attemptCtx, inst)
	case leftoverAddedDatabase:
		return false, s.retryAddedDatabase(attemptCtx, inst)
	case leftoverAddBackups:
		return false, s.retryAddBackups(attemptCtx, inst, inst.RollbackLog)
	case leftoverRestore:
		return s.retryRestoreNamespace(attemptCtx, inst)
	case leftoverRestoreDeletion:
		return true, nil
	default:
		return false, nil
	}
}

func (s *ProvisioningService) retryCleanup(inst *domain.DatabaseInstance, name string, err error) error {
	if err != nil {
		return err
	}
	return s.store.RecordRollbackRetry(inst.ProjectID, rollbackLogWithCleanupDone(inst.RollbackLog, name), inst.FailureReason, inst.Status)
}

func (s *ProvisioningService) retrySingleHostBuild(ctx context.Context, inst *domain.DatabaseInstance) error {
	prov, ok := s.factory.Get(inst.DBType)
	if !ok {
		return fmt.Errorf("no provisioner for database type %s", inst.DBType)
	}
	return s.retryCleanup(inst, removeFailedBuildCleanup(inst.ProjectID), prov.Deprovision(ctx, inst.Namespace, inst.ProjectID))
}

func (s *ProvisioningService) retryAddedDatabase(ctx context.Context, inst *domain.DatabaseInstance) error {
	remover, err := s.databaseRemover(inst.DBType)
	if err != nil {
		return err
	}
	if err := remover.RemoveDatabase(ctx, inst.Namespace, inst.ProjectID); err != nil {
		return err
	}
	rollbackLog := rollbackLogWithCleanupDone(inst.RollbackLog, removeDatabaseCleanup(inst.Namespace))
	if strings.Contains(inst.FailureReason, purgeWaitingNote) {
		return s.retryAddBackups(ctx, inst, rollbackLog)
	}
	return s.store.RecordRollbackRetry(inst.ProjectID, rollbackLog, inst.FailureReason, inst.Status)
}

// retryAddBackups purges what the failed add archived, now its cluster is gone.
// Project ids are not reused and a project without a database backs nothing up,
// so everything under its prefix is the failed add's.
func (s *ProvisioningService) retryAddBackups(ctx context.Context, inst *domain.DatabaseInstance, rollbackLog string) error {
	reason := inst.FailureReason
	for _, note := range []string{purgeWaitingNote, purgeFailedNote, purgeNoPurgerNote} {
		reason = strings.ReplaceAll(reason, note, "")
	}
	if s.backupPurger == nil {
		return s.store.RecordRollbackRetry(inst.ProjectID, rollbackLog, reason+purgeNoPurgerNote, inst.Status)
	}
	if err := s.purgeAddedDatabaseBackups(ctx, inst); err != nil {
		if recordErr := s.store.RecordRollbackRetry(inst.ProjectID, rollbackLog, reason+purgeFailedNote, inst.Status); recordErr != nil {
			return errors.Join(err, recordErr)
		}
		return err
	}
	return s.store.RecordRollbackRetry(inst.ProjectID, rollbackLog, reason, inst.Status)
}

// retryRestoreNamespace asks for the copy's namespace to go and reports when it
// has. It never waits under the lease; a namespace still terminating is the next
// attempt's.
func (s *ProvisioningService) retryRestoreNamespace(ctx context.Context, inst *domain.DatabaseInstance) (bool, error) {
	if err := s.k8sClient.DeleteNamespace(ctx, inst.Namespace); err != nil {
		return false, err
	}
	exists, err := s.k8sClient.NamespaceExists(ctx, inst.Namespace)
	if err != nil {
		return false, err
	}
	if exists {
		return false, fmt.Errorf("%w: namespace %s is terminating", errRollbackInProgress, inst.Namespace)
	}
	return true, nil
}

func parseRollbackLog(rollbackLog string) []provisioner.CleanupResult {
	results, err := provisioner.ParseCleanupResults(rollbackLog)
	if err != nil {
		return nil
	}
	return results
}

func rollbackLogWithCleanupDone(rollbackLog, name string) string {
	results := parseRollbackLog(rollbackLog)
	updated := make([]provisioner.CleanupResult, 0, len(results))
	for _, result := range results {
		if result.Name == name {
			result = provisioner.CleanupResult{Name: name, OK: true}
		}
		updated = append(updated, result)
	}
	encoded, err := provisioner.EncodeCleanupResults(updated)
	if err != nil {
		log.Printf("WARN: marshal rollback log: %v", err)
		return rollbackLog
	}
	return encoded
}

// rollbackRetrySchedule keeps each project's next attempt. It lives in the
// process: a new leader starts every project afresh, which only costs one attempt.
type rollbackRetrySchedule struct {
	mu           sync.Mutex
	now          func() time.Time
	attemptLimit time.Duration
	next         map[string]retryBackoff
}

type retryBackoff struct {
	delay time.Duration
	at    time.Time
}

func (r *rollbackRetrySchedule) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *rollbackRetrySchedule) limit() time.Duration {
	if r.attemptLimit > 0 {
		return r.attemptLimit
	}
	return rollbackRetryAttemptLimit
}

func (r *rollbackRetrySchedule) due(projectID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, waiting := r.next[projectID]
	return !waiting || !r.clock().Before(entry.at)
}

func (r *rollbackRetrySchedule) failed(projectID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.next == nil {
		r.next = map[string]retryBackoff{}
	}
	delay := RollbackRetryInterval
	if entry, waiting := r.next[projectID]; waiting {
		delay = min(2*entry.delay, rollbackRetryMaxDelay)
	}
	r.next[projectID] = retryBackoff{delay: delay, at: r.clock().Add(delay - r.limit() - rollbackRetrySlack)}
}

func (r *rollbackRetrySchedule) succeeded(projectID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.next, projectID)
}

// StartRollbackRetrySweeper retries now and every interval, as leader only. It
// returns the stop.
func (s *ProvisioningService) StartRollbackRetrySweeper(ctx context.Context, leader LeaderChecker, every time.Duration) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			if isLeader(ctx, leader) {
				if finished := s.RetryFailedRollbacks(ctx); len(finished) > 0 {
					log.Printf("finished failed rollbacks: %v", finished)
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return cancel
}
