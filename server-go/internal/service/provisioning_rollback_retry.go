package service

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// RollbackRetryInterval is how often a cleanup that failed is tried again.
const RollbackRetryInterval = 5 * time.Minute

// The provisioners name these cleanups; a rollback log is matched against them.
func deleteNamespaceCleanup(namespace string) string { return "delete namespace " + namespace }

func removeDatabaseCleanup(namespace string) string {
	return "remove database from namespace " + namespace
}

// RetryFailedRollbacks finishes the rollbacks a failed cleanup left short: the
// namespace of a FAILED project, or the database of an add that went back to no
// database. It answers with the projects it finished. Only a rollback log naming
// that very cleanup as failed licenses it, because FAILED alone proves nothing: a
// major upgrade can leave a live project FAILED with its data in the namespace.
func (s *ProvisioningService) RetryFailedRollbacks(ctx context.Context) []string {
	if s.k8sClient == nil {
		return nil
	}
	all, err := s.store.FindAll()
	if err != nil {
		log.Printf("ERROR: list projects to retry failed rollbacks: %v", err)
		return nil
	}
	var finished []string
	for _, inst := range all {
		if leftoverCleanupName(inst) == "" {
			continue
		}
		err := s.retryRollback(ctx, inst.ProjectID)
		if errors.Is(err, ErrProjectOperationRunning) {
			continue
		}
		if err != nil {
			log.Printf("ERROR: retry the failed rollback of %s: %v", inst.ProjectID, err)
			continue
		}
		finished = append(finished, inst.ProjectID)
	}
	return finished
}

// retryRollback judges the row again under the project's lease, which excludes
// a delete, a pause or a new add that started since the listing.
func (s *ProvisioningService) retryRollback(ctx context.Context, projectID string) error {
	inst, release, err := s.holdProject(ctx, projectID, OperationRollbackRetry, func(string, string) error { return nil })
	if err != nil {
		return err
	}
	defer release()
	name := leftoverCleanupName(inst)
	if name == "" {
		return nil
	}
	cleanupCtx, cancel := provisioner.CleanupContext(ctx)
	defer cancel()
	if err := s.runLeftoverCleanup(cleanupCtx, inst); err != nil {
		return err
	}
	inst.RollbackLog = rollbackLogWithCleanupDone(inst.RollbackLog, name)
	return s.store.UpdateIfStatus(inst, inst.Status)
}

func (s *ProvisioningService) runLeftoverCleanup(ctx context.Context, inst *domain.DatabaseInstance) error {
	if inst.NoDatabase && inst.Status == string(domain.StatusActive) {
		remover, err := s.databaseRemover(inst.DBType)
		if err != nil {
			return err
		}
		return remover.RemoveDatabase(ctx, inst.Namespace, inst.ProjectID)
	}
	return s.k8sClient.DeleteNamespace(ctx, inst.Namespace)
}

// leftoverCleanupName names the failed cleanup a retry would finish, or nothing.
func leftoverCleanupName(inst *domain.DatabaseInstance) string {
	if inst.DeploymentMode != domain.ModeK8s {
		return ""
	}
	name := ""
	switch {
	case inst.Status == string(domain.StageFailed):
		name = deleteNamespaceCleanup(inst.Namespace)
	case inst.Status == string(domain.StatusActive) && inst.NoDatabase:
		name = removeDatabaseCleanup(inst.Namespace)
	default:
		return ""
	}
	for _, result := range parseRollbackLog(inst.RollbackLog) {
		if result.Name == name && !result.OK {
			return name
		}
	}
	return ""
}

func parseRollbackLog(rollbackLog string) []provisioner.CleanupResult {
	var results []provisioner.CleanupResult
	if rollbackLog == "" || json.Unmarshal([]byte(rollbackLog), &results) != nil {
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
	encoded, err := json.Marshal(updated)
	if err != nil {
		log.Printf("WARN: marshal rollback log: %v", err)
		return rollbackLog
	}
	return string(encoded)
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
