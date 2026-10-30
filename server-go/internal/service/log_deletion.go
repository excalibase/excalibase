package service

import (
	"context"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/logdelete"
)

// LogDeletionQueue keeps deleted projects' log deletions until Loki has them (EXC-678).
type LogDeletionQueue interface {
	EnqueueProjectLogDeletion(ctx context.Context, d logdelete.Deletion) error
}

type projectLogDeletion struct {
	queue LogDeletionQueue
	audit AuditWriter
	// maxAge is the longest any line is kept, which bounds a deletion's window.
	maxAge time.Duration
}

// SetLogDeletion queues each deleted project's stored log lines for removal;
// audit may be nil. Unset, the teardown has no such step.
func (s *ProvisioningService) SetLogDeletion(queue LogDeletionQueue, audit AuditWriter, maxAge time.Duration) {
	s.logDeletion = projectLogDeletion{queue: queue, audit: audit, maxAge: maxAge}
}

// queueLogDeletion is the teardown step. A project with no selector that is
// surely its own alone is skipped and audited: its logs are never worth a
// wider selector, nor a project that cannot be deleted.
func (s *ProvisioningService) queueLogDeletion(ctx context.Context, inst *domain.DatabaseInstance) error {
	now := s.deletionClock()
	selector, err := logdelete.ProjectSelector(inst)
	if err != nil {
		log.Printf("log deletion of %s skipped: %v", inst.ProjectID, err)
		s.auditLogDeletion(ctx, logdelete.ActionSkipped, logdelete.Deletion{ProjectID: inst.ProjectID, LastError: err.Error()}, now)
		return nil
	}
	var created time.Time
	if inst.CreatedAt != nil {
		created = inst.CreatedAt.Time
	}
	row := logdelete.Deletion{
		ProjectID: inst.ProjectID, Selector: selector, NextAttemptAt: now,
		Start: logdelete.StartFor(created, now, s.logDeletion.maxAge),
	}
	if err := s.logDeletion.queue.EnqueueProjectLogDeletion(ctx, row); err != nil {
		return err
	}
	s.auditLogDeletion(ctx, logdelete.ActionQueued, row, now)
	return nil
}

func (s *ProvisioningService) auditLogDeletion(ctx context.Context, action string, row logdelete.Deletion, at time.Time) {
	if s.logDeletion.audit == nil {
		return
	}
	entry := logdelete.AuditEntry(action, row, at)
	if action == logdelete.ActionSkipped {
		entry.Details = "reason=" + row.LastError
	}
	if err := s.logDeletion.audit.LogAudit(ctx, entry); err != nil {
		log.Printf("log deletion of %s: audit %s: %v", row.ProjectID, action, err)
	}
}
