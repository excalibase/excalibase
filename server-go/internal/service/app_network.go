package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// OperationAppNetwork names the lease a private-network change takes on its project.
const OperationAppNetwork ProjectOperation = "app private network change"

var (
	ErrAppNetworkProjectNotFound = errors.New("project not found")
	ErrAppNetworkUnsupported     = errors.New("this project's apps do not run on Kubernetes, so there is no network to open")
)

// AppNetworkProjectFinder finds the project a setting belongs to.
type AppNetworkProjectFinder interface {
	FindByProjectID(projectID string) (*domain.DatabaseInstance, error)
}

// AppNetworkView is the setting as recorded and as the cluster holds it.
type AppNetworkView struct {
	ProjectID      string
	PrivateNetwork bool
	Applied        bool
}

// AppNetworkService turns a project's private network between its apps on and
// off (EXC-524). The cluster changes first and the setting is written after,
// so "on" is never recorded for a policy that does not exist, and a failed
// write never leaves the network open under a setting that says off.
type AppNetworkService struct {
	settings storage.ProjectAppNetworkStore
	projects AppNetworkProjectFinder
	kube     k8s.KubeClient
	claimer  ProjectOperationClaimer
}

func NewAppNetworkService(settings storage.ProjectAppNetworkStore, projects AppNetworkProjectFinder,
	kube k8s.KubeClient, claimer ProjectOperationClaimer) *AppNetworkService {
	return &AppNetworkService{settings: settings, projects: projects, kube: kube, claimer: claimerOrInProcess(claimer)}
}

func (s *AppNetworkService) Describe(ctx context.Context, projectID string) (AppNetworkView, error) {
	inst, err := s.project(projectID)
	if err != nil {
		return AppNetworkView{}, err
	}
	return s.view(ctx, inst)
}

func (s *AppNetworkService) Set(ctx context.Context, projectID string, enabled bool) (AppNetworkView, error) {
	if _, err := s.project(projectID); err != nil {
		return AppNetworkView{}, err
	}
	release, claimed, err := s.claimer.Claim(ctx, projectID, OperationAppNetwork)
	if err != nil {
		return AppNetworkView{}, fmt.Errorf("claim project for %s: %w", OperationAppNetwork, err)
	}
	if !claimed {
		return AppNetworkView{}, fmt.Errorf("%w (%s)", ErrProjectOperationRunning, projectID)
	}
	defer release()

	inst, err := s.project(projectID)
	if err != nil {
		return AppNetworkView{}, err
	}
	if enabled {
		err = s.open(ctx, inst)
	} else {
		err = s.close(ctx, inst)
	}
	if err != nil {
		return AppNetworkView{}, err
	}
	return s.view(ctx, inst)
}

func (s *AppNetworkService) open(ctx context.Context, inst *domain.DatabaseInstance) error {
	if inst.Status != string(domain.StatusActive) {
		return fmt.Errorf("project %s is %s; %w", inst.ProjectID, inst.Status, ErrProjectNotActive)
	}
	if err := s.kube.SetAppPrivateNetwork(ctx, inst.Namespace, true); err != nil {
		return fmt.Errorf("open the app private network: %w", err)
	}
	if err := s.settings.SetAppPrivateNetwork(ctx, inst.ProjectID, true); err != nil {
		if closeErr := s.kube.SetAppPrivateNetwork(ctx, inst.Namespace, false); closeErr != nil {
			return errors.Join(err, fmt.Errorf("close the network the setting could not record: %w", closeErr))
		}
		return err
	}
	return nil
}

// close is allowed in any project state: it only ever narrows what apps accept.
func (s *AppNetworkService) close(ctx context.Context, inst *domain.DatabaseInstance) error {
	if err := s.kube.SetAppPrivateNetwork(ctx, inst.Namespace, false); err != nil {
		return fmt.Errorf("close the app private network: %w", err)
	}
	return s.settings.SetAppPrivateNetwork(ctx, inst.ProjectID, false)
}

func (s *AppNetworkService) view(ctx context.Context, inst *domain.DatabaseInstance) (AppNetworkView, error) {
	enabled, err := s.settings.GetAppPrivateNetwork(ctx, inst.ProjectID)
	if err != nil {
		return AppNetworkView{}, err
	}
	applied, err := s.kube.AppPrivateNetworkOpen(ctx, inst.Namespace)
	if err != nil {
		return AppNetworkView{}, err
	}
	return AppNetworkView{ProjectID: inst.ProjectID, PrivateNetwork: enabled, Applied: applied}, nil
}

func (s *AppNetworkService) project(projectID string) (*domain.DatabaseInstance, error) {
	inst, err := s.projects.FindByProjectID(projectID)
	if err != nil {
		return nil, fmt.Errorf("read project %s: %w", projectID, err)
	}
	if inst == nil {
		return nil, fmt.Errorf("%w: %s", ErrAppNetworkProjectNotFound, projectID)
	}
	if inst.DeploymentMode != domain.ModeK8s || inst.Namespace == "" {
		return nil, ErrAppNetworkUnsupported
	}
	return inst, nil
}
