package service

import (
	"context"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

type ProvisioningService struct {
	store   storage.InstanceStore
	factory *provisioner.Factory
	hooks   *edgefn.HookService // optional — nil if edge functions not configured
}

func NewProvisioningService(store storage.InstanceStore, factory *provisioner.Factory) *ProvisioningService {
	return &ProvisioningService{store: store, factory: factory}
}

func (s *ProvisioningService) SetHookService(hooks *edgefn.HookService) {
	s.hooks = hooks
}

func (s *ProvisioningService) Provision(ctx context.Context, req domain.ProvisioningRequest) (*domain.ProvisioningResponse, error) {
	// Check for duplicate
	existing, _ := s.store.FindByProjectID(req.ProjectName)
	if existing != nil {
		return nil, fmt.Errorf("project ID already exists: %s", req.ProjectName)
	}

	// Get tier config
	tier, err := config.GetTierConfig(req.Tier)
	if err != nil {
		return nil, err
	}

	// Get provisioner
	prov, ok := s.factory.Get(req.DBType)
	if !ok {
		return nil, fmt.Errorf("unsupported database type: %s", req.DBType)
	}

	// Create initial instance record
	now := &domain.FlexTime{Time: time.Now()}
	namespace := fmt.Sprintf("%s-%s", req.OrgID, req.ProjectName)
	inst := &domain.DatabaseInstance{
		ProjectID:    req.ProjectName,
		OrgID:        req.OrgID,
		OwnerID:      req.OwnerID,
		DBType:       req.DBType,
		Tier:         req.Tier,
		Namespace:    namespace,
		Status:       "PROVISIONING",
		CurrentStage: domain.StageValidating,
		CreatedAt:    now,
	}

	if req.Backup != nil {
		inst.BackupEnabled = boolPtr(req.Backup.Enabled)
		inst.BackupSchedule = req.Backup.Schedule
		inst.BackupRetentionDays = intPtr(req.Backup.Retention)
	}
	if req.Tags != nil {
		tagsJSON, _ := fmt.Printf("%v", req.Tags) // will be replaced with proper JSON
		_ = tagsJSON
	}

	// Save initial state
	s.store.Save(inst)

	// Stage callback updates instance in store
	cb := func(stage domain.ProvisioningStage) {
		inst.CurrentStage = stage
		ft := &domain.FlexTime{Time: time.Now()}
		inst.UpdatedAt = ft
		s.store.Save(inst)
	}

	// Pre-provision hooks (non-blocking)
	if s.hooks != nil {
		s.hooks.ExecuteHooksAsync(ctx, "pre-provision", edgefn.HookContext{
			ProjectID: req.ProjectName, OrgID: req.OrgID,
			DatabaseType: string(req.DBType), Tier: string(req.Tier),
		}, nil)
	}

	// Run provisioning
	result, err := prov.Provision(ctx, req, tier, cb)
	if err != nil {
		inst.Status = "FAILED"
		inst.CurrentStage = domain.StageFailed
		inst.FailureReason = err.Error()
		s.store.Save(inst)
		return &domain.ProvisioningResponse{
			ProjectID:    req.ProjectName,
			Status:       "FAILED",
			CurrentStage: domain.StageFailed,
			Namespace:    namespace,
			FailureReason: err.Error(),
			CreatedAt:    inst.CreatedAt,
		}, nil
	}

	// Update with connection details
	port := result.Port
	inst.Host = result.Host
	inst.ReadOnlyHost = result.ReadOnlyHost
	inst.Port = &port
	inst.DatabaseName = result.DatabaseName
	inst.Username = result.Username
	inst.Password = result.Password
	inst.SSLMode = result.SSLMode
	inst.Status = "ACTIVE"
	inst.CurrentStage = domain.StageCompleted
	inst.MetricsEndpoint = fmt.Sprintf("http://%s-postgres-1.%s.svc.cluster.local:9187/metrics", req.ProjectName, namespace)

	delProtection := false
	inst.DeletionProtection = &delProtection
	poolerEnabled := false
	inst.PoolerEnabled = &poolerEnabled

	finalNow := &domain.FlexTime{Time: time.Now()}
	inst.UpdatedAt = finalNow
	inst.LastHealthCheck = finalNow
	s.store.Save(inst)

	// Post-provision hooks (non-blocking, with credentials)
	if s.hooks != nil {
		s.hooks.ExecuteHooksAsync(ctx, "post-provision", edgefn.HookContext{
			ProjectID: req.ProjectName, OrgID: req.OrgID,
			DatabaseType: string(req.DBType), Tier: string(req.Tier),
			Host: result.Host, Port: result.Port,
			Database: result.DatabaseName, Username: result.Username, Password: result.Password,
		}, nil)
	}

	return &domain.ProvisioningResponse{
		ProjectID:    req.ProjectName,
		Status:       "ACTIVE",
		CurrentStage: domain.StageCompleted,
		Namespace:    namespace,
		Host:         result.Host,
		Port:         &port,
		DatabaseName: result.DatabaseName,
		CreatedAt:    inst.CreatedAt,
	}, nil
}

func (s *ProvisioningService) Deprovision(ctx context.Context, projectID string) error {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return fmt.Errorf("project not found: %s", projectID)
	}

	if inst.DeletionProtection != nil && *inst.DeletionProtection {
		return fmt.Errorf("deletion protection is enabled for %s", projectID)
	}

	prov, ok := s.factory.Get(inst.DBType)
	if ok {
		if err := prov.Deprovision(ctx, inst.Namespace, projectID); err != nil {
			fmt.Printf("WARN: K8s deprovision failed for %s: %v\n", projectID, err)
		}
	}

	return s.store.Delete(projectID)
}

func (s *ProvisioningService) GetInstance(projectID string) (*domain.DatabaseInstance, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil {
		return nil, err
	}
	if inst == nil {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}
	return inst, nil
}

func (s *ProvisioningService) GetAllInstances() ([]*domain.DatabaseInstance, error) {
	return s.store.FindAll()
}

func (s *ProvisioningService) GetInstancesByOwner(ownerID string) ([]*domain.DatabaseInstance, error) {
	return s.store.FindByOwner(ownerID)
}

func (s *ProvisioningService) GetCredentials(projectID string) (*domain.CredentialsResponse, error) {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return nil, err
	}

	port := 5432
	if inst.Port != nil {
		port = *inst.Port
	}

	connURL := fmt.Sprintf("postgresql://%s:%s@%s:%d/%s?sslmode=%s",
		inst.Username, inst.Password, inst.Host, port, inst.DatabaseName, inst.SSLMode)

	return &domain.CredentialsResponse{
		ProjectID:     projectID,
		Host:          inst.Host,
		ReadOnlyHost:  inst.ReadOnlyHost,
		Port:          port,
		DatabaseName:  inst.DatabaseName,
		Username:      inst.Username,
		Password:      inst.Password,
		SSLMode:       inst.SSLMode,
		ConnectionURL: connURL,
	}, nil
}

func (s *ProvisioningService) SetDeletionProtection(projectID string, enabled bool) error {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return err
	}
	inst.DeletionProtection = &enabled
	return s.store.Save(inst)
}

func boolPtr(b bool) *bool  { return &b }
func intPtr(i int) *int     { return &i }
