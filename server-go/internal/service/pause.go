package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// PauseService transitions a project ACTIVE → PAUSING → backup →
// workload-stop → PAUSED, and the reverse on Resume. Backup is
// mandatory: if it fails, the project stays ACTIVE rather than being
// stopped without a recovery point. Provisioner.Pause failure leaves
// the project in PAUSING so operators see a stuck state instead of
// silent ACTIVE-with-stopped-workload.
type PauseService struct {
	instances storage.InstanceStore
	pausers   map[domain.DeploymentMode]provisioner.Pauser
	backups   BackupTrigger

	mu sync.Mutex
}

// BackupTrigger is the slice of BackupService we depend on. Defined
// as an interface so tests can fake it without bringing the whole
// adapter wiring along.
type BackupTrigger interface {
	TriggerManualBackup(ctx context.Context, projectID string) (map[string]interface{}, error)
}

// PauseServiceConfig wires the dependencies. All three must be set.
type PauseServiceConfig struct {
	Instances storage.InstanceStore
	Pausers   map[domain.DeploymentMode]provisioner.Pauser
	Backups   BackupTrigger
}

func NewPauseService(c PauseServiceConfig) *PauseService {
	return &PauseService{
		instances: c.Instances,
		pausers:   c.Pausers,
		backups:   c.Backups,
	}
}

// ErrPauseUnsupported is returned when a project's deployment mode
// has no Pauser registered (BYOC, primarily — operator owns the DB
// lifecycle, we don't touch it).
var ErrPauseUnsupported = errors.New("pause not supported for this deployment mode")

// Pause atomically transitions the project to PAUSED, taking a backup
// first. Idempotent: already-PAUSED projects are returned without
// touching backup or workload.
func (s *PauseService) Pause(ctx context.Context, projectID, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	inst, err := s.instances.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return fmt.Errorf("project not found: %s", projectID)
	}
	if inst.Status == string(domain.StatusPaused) {
		return nil // idempotent
	}
	pauser, ok := s.pausers[inst.DeploymentMode]
	if !ok {
		return ErrPauseUnsupported
	}

	// 1. Take a backup. If this fails the project stays ACTIVE.
	if s.backups != nil {
		if _, err := s.backups.TriggerManualBackup(ctx, projectID); err != nil {
			return fmt.Errorf("pause: pre-pause backup failed (project stays ACTIVE): %w", err)
		}
	}

	// 2. Mark PAUSING + persist so operators see in-flight state.
	inst.Status = string(domain.StatusPausing)
	inst.PauseReason = reason
	inst.UpdatedAt = &domain.FlexTime{Time: time.Now()}
	if err := s.instances.Update(inst); err != nil {
		log.Printf("WARN: persist PAUSING for %s: %v", projectID, err)
	}

	// 3. Stop the workload. On failure leave PAUSING — operator sees
	// a stuck state, can investigate before retrying.
	if err := pauser.Pause(ctx, inst.Namespace, projectID); err != nil {
		return fmt.Errorf("pause: provisioner stop failed (status remains PAUSING): %w", err)
	}

	// 4. Stamp PAUSED.
	inst.Status = string(domain.StatusPaused)
	inst.UpdatedAt = &domain.FlexTime{Time: time.Now()}
	return s.instances.Update(inst)
}

// Resume transitions PAUSED → RESUMING → workload-start → ACTIVE.
// Idempotent: ACTIVE / RESUMING projects are returned without touching
// the workload. Capacity / tier checks are the caller's responsibility
// — for now the handler runs them; a future hook here could enforce
// platform-side.
func (s *PauseService) Resume(ctx context.Context, projectID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	inst, err := s.instances.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return fmt.Errorf("project not found: %s", projectID)
	}
	if inst.Status != string(domain.StatusPaused) {
		// Idempotent: ACTIVE or anything else, no-op.
		return nil
	}
	pauser, ok := s.pausers[inst.DeploymentMode]
	if !ok {
		return ErrPauseUnsupported
	}

	inst.Status = string(domain.StatusResuming)
	inst.UpdatedAt = &domain.FlexTime{Time: time.Now()}
	_ = s.instances.Update(inst)

	if err := pauser.Resume(ctx, inst.Namespace, projectID); err != nil {
		return fmt.Errorf("resume: provisioner start failed (status remains RESUMING): %w", err)
	}

	inst.Status = "ACTIVE"
	inst.PauseReason = ""
	inst.LastActiveAt = &domain.FlexTime{Time: time.Now()}
	inst.UpdatedAt = inst.LastActiveAt
	return s.instances.Update(inst)
}
