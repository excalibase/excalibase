package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/pgroles"
	"github.com/excalibase/provisioning-poc/internal/tenantcert"
)

var _ InPlaceProjects = (*ProvisioningService)(nil)

// ErrProjectNotRestorable refuses an in-place restore of a project that is
// neither running nor left stopped by an earlier restore.
var ErrProjectNotRestorable = errors.New("only a running project, or one an earlier restore left stopped, can be restored in place")

// HoldForInPlaceRestore takes the project's lifecycle lease for an in-place
// restore. See InPlaceProjects.
func (s *ProvisioningService) HoldForInPlaceRestore(ctx context.Context, projectID string) (*domain.DatabaseInstance, func(), error) {
	inst, release, err := s.holdProject(ctx, projectID, OperationRestore, admitInPlaceRestore)
	if err != nil {
		return nil, nil, err
	}
	if inst.NoDatabase {
		release()
		return nil, nil, fmt.Errorf("%w: %s", domain.ErrNoDatabase, projectID)
	}
	// A RESTORING project may be one a copy is still being built into; only
	// one an earlier restore gave up on is this restore's to take over.
	if inst.Status == string(domain.StatusRestoring) && inst.CurrentStep != domain.RestoreStepRestoreStopped {
		release()
		return nil, nil, fmt.Errorf("%w: %s is being restored", ErrProjectNotRestorable, projectID)
	}
	return inst, release, nil
}

func admitInPlaceRestore(projectID, status string) error {
	if status == string(domain.StatusActive) || status == string(domain.StatusRestoring) {
		return nil
	}
	return fmt.Errorf("%w: %s is %s", ErrProjectNotRestorable, projectID, status)
}

// StopReplication removes the project's CDC watcher. See InPlaceProjects.
func (s *ProvisioningService) StopReplication(ctx context.Context, inst *domain.DatabaseInstance) error {
	pg, err := s.postgresProvisioner()
	if err != nil {
		return err
	}
	return pg.StopReplication(ctx, inst.Namespace, inst.ProjectID)
}

// AnnounceDatabaseReplaced publishes a schema change for the project, which
// evicts every cache the data plane keeps of its tables. See InPlaceProjects.
func (s *ProvisioningService) AnnounceDatabaseReplaced(ctx context.Context, projectID string) {
	if s.projectEvents == nil {
		return
	}
	s.projectEvents.PublishPolicyChange(ctx, domain.PolicyChangeEvent{
		ProjectID: projectID,
		Kind:      domain.SchemaChangeKind,
		Op:        domain.OpChangeUpdate,
	})
}

// ReapplyCredentials sets every password-authenticated role the project has
// on file back to its filed password, in one batch on the primary. Roles that
// sign in with a client certificate are left alone: the kept CA still signs
// their certificates. See InPlaceProjects.
func (s *ProvisioningService) ReapplyCredentials(ctx context.Context, inst *domain.DatabaseInstance) error {
	if s.vault == nil || s.k8sClient == nil {
		return ErrCredentialRotationUnavailable
	}
	filed, err := s.filedCredentialPaths(inst.ProjectID)
	if err != nil {
		return err
	}
	roles := []RolePassword{}
	for _, role := range []string{roleAdmin, roleAuthAdmin, roleApp, roleWatcher} {
		path := vaultCredentialPath(inst.ProjectID, role)
		if !filed[path] {
			continue
		}
		record, err := s.vault.Get(path)
		if err != nil {
			return fmt.Errorf("read %s credential: %w", role, err)
		}
		if _, err := tenantcert.FromRecord(record); err == nil || record["password"] == "" {
			continue
		}
		roles = append(roles, RolePassword{Role: record["username"], Password: record["password"]})
	}
	if len(roles) == 0 {
		return nil
	}
	statement := pgroles.PinnedSearchPath + BuildProjectRoleResetSQL(roles) + "\n"
	_, err = s.k8sClient.ExecInPodStdin(ctx, inst.Namespace, inst.ProjectID+primaryPodSuffix, "postgres",
		[]string{"psql", "-U", "postgres", "-q", "-v", "ON_ERROR_STOP=1", "-f", "-"}, statement)
	if err != nil {
		message := err.Error()
		for _, role := range roles {
			message = redactSecret(message, role.Password)
		}
		return errors.New("set the filed passwords back: " + message)
	}
	return nil
}
