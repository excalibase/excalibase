package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/appregistry"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// PullGenerationRotator moves a project to its next pull-password generation.
type PullGenerationRotator interface {
	RotatePull(projectID string) (int, error)
}

// PullSecretRefresher rewrites the pull secrets already rendered for one registry.
type PullSecretRefresher interface {
	RefreshRegistryPullSecrets(ctx context.Context, namespace, projectID string, auth k8s.RegistryAuth) ([]string, error)
}

// PullRotationService retires a project's internal-registry pull password
// and re-renders its running apps' pull secrets, so no app needs a redeploy
// to pull again (EXC-540).
type PullRotationService struct {
	generations PullGenerationRotator
	internal    *appregistry.Internal
	instances   storage.InstanceStore
	runtime     PullSecretRefresher
}

func NewPullRotationService(generations PullGenerationRotator, internal *appregistry.Internal,
	instances storage.InstanceStore, runtime PullSecretRefresher) *PullRotationService {
	return &PullRotationService{generations: generations, internal: internal, instances: instances, runtime: runtime}
}

// StalePullSecretsError is a rotation that took effect while some apps' pull
// secrets still hold the old password; Apps is empty when which is unknown.
type StalePullSecretsError struct {
	Generation int
	Apps       []string
	Err        error
}

func (e *StalePullSecretsError) Error() string {
	if len(e.Apps) == 0 {
		return fmt.Sprintf("pull credential rotated to generation %d, but the apps' pull secrets were not updated: %v", e.Generation, e.Err)
	}
	return fmt.Sprintf("pull credential rotated to generation %d, but these apps' pull secrets still hold the old one: %s: %v",
		e.Generation, strings.Join(e.Apps, ", "), e.Err)
}

func (e *StalePullSecretsError) Unwrap() error { return e.Err }

// RotatePull answers the new generation; an error after the bump is a *StalePullSecretsError.
func (s *PullRotationService) RotatePull(ctx context.Context, projectID string) (int, error) {
	namespace, err := s.appNamespace(projectID)
	if err != nil {
		return 0, err
	}
	gen, err := s.generations.RotatePull(projectID)
	if err != nil || namespace == "" {
		return gen, err
	}
	if stale, err := s.refresh(ctx, projectID, namespace); err != nil {
		return gen, &StalePullSecretsError{Generation: gen, Apps: stale, Err: err}
	}
	return gen, nil
}

// appNamespace is "" for a project with nowhere to run apps.
func (s *PullRotationService) appNamespace(projectID string) (string, error) {
	inst, err := s.instances.FindByProjectID(projectID)
	if err != nil {
		return "", fmt.Errorf("look up project namespace: %w", err)
	}
	if inst == nil {
		return "", nil
	}
	return inst.Namespace, nil
}

// refresh writes the credential a deploy would write now into every app's pull secret.
func (s *PullRotationService) refresh(ctx context.Context, projectID, namespace string) ([]string, error) {
	pull, err := s.internal.PullCredential(projectID)
	if err != nil {
		return nil, err
	}
	auth := k8s.RegistryAuth{Registry: s.internal.Host(), Username: pull.Username, Password: pull.Password}
	return s.runtime.RefreshRegistryPullSecrets(ctx, namespace, projectID, auth)
}
