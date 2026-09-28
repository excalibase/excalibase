package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// EXC-426: a project is a container of services, and a database is one of
// them. A project created without one holds its namespace, quota and org
// slot, and a database can be added later through the same pipeline a
// project with a database is created by.

var (
	// ErrDatabaseSettingsWithoutDatabase refuses a project created without a
	// database whose request still names database settings: one of the two
	// was not meant, and the platform does not guess which.
	ErrDatabaseSettingsWithoutDatabase = errors.New("a project without a database takes no database settings")
	// ErrNoDatabaseNeedsKubernetes refuses a project without a database on a
	// docker install: there the project is its database container, and there
	// is no namespace for apps to live in.
	ErrNoDatabaseNeedsKubernetes = errors.New("a project without a database needs the Kubernetes deployment mode")
	// ErrAddDatabaseRequest refuses an add-database request that carries
	// project fields or asks for no database.
	ErrAddDatabaseRequest = errors.New("adding a database takes only database settings")
)

// OperationAddDatabase is the lease an add-database holds for its whole build.
const OperationAddDatabase ProjectOperation = "database add"

// databaseSettingsNamed reports whether a request names anything about a
// database.
func databaseSettingsNamed(req domain.ProvisioningRequest) bool {
	return req.DBType != "" || strings.TrimSpace(req.PostgresVersion) != "" || req.DocumentDB ||
		req.Backup != nil || req.Pooler != nil || req.Network != nil || req.Maintenance != nil ||
		len(req.Parameters) > 0 || req.StorageClass != "" || req.DatabaseName != "" ||
		req.MasterUsername != "" || req.ParameterGroup != ""
}

// provisionWithoutDatabase creates a project that holds no database: the row
// (the org slot, counted exactly like any other project) and a namespace
// fenced and capped the same way every project namespace is.
func (s *ProvisioningService) provisionWithoutDatabase(ctx context.Context, req domain.ProvisioningRequest) (*domain.ProvisioningResponse, error) {
	if err := validateProjectIdentity(req); err != nil {
		return nil, err
	}
	if databaseSettingsNamed(req) {
		return nil, ErrDatabaseSettingsWithoutDatabase
	}
	if s.deploymentMode() != domain.ModeK8s || s.k8sClient == nil {
		return nil, ErrNoDatabaseNeedsKubernetes
	}
	tierType, err := s.orgTier(ctx, req.OrgID)
	if err != nil {
		return nil, err
	}
	if err := s.EnsureOrgProjectCapacity(ctx, req.OrgID, tierType); err != nil {
		return nil, err
	}
	projectRef, err := s.generateUniqueProjectRef()
	if err != nil {
		return nil, err
	}
	inst := &domain.DatabaseInstance{
		ProjectID:      projectRef,
		ProjectName:    req.ProjectName,
		OrgID:          req.OrgID,
		OwnerID:        req.OwnerID,
		Tier:           tierType,
		DeploymentMode: domain.ModeK8s,
		Namespace:      fmt.Sprintf("%s-%s", req.OrgID, projectRef),
		NoDatabase:     true,
		Status:         string(domain.StatusProvisioning),
		CurrentStage:   domain.StageNamespaceCreation,
		CreatedAt:      &domain.FlexTime{Time: time.Now()},
	}
	if err := s.createProjectRow(ctx, inst, tierType); err != nil {
		return nil, err
	}
	if err := s.k8sClient.CreateProjectNamespace(ctx, inst.Namespace, inst.OrgID); err != nil {
		return s.failProjectWithoutDatabase(ctx, inst, "create namespace", err), nil
	}
	markProjectActive(inst)
	if err := s.store.Update(inst); err != nil {
		return s.failProjectWithoutDatabase(ctx, inst, "record project", err), nil
	}
	s.announceProject(ctx, inst)
	return projectResponse(inst), nil
}

// failProjectWithoutDatabase removes the namespace a failed create made and
// leaves the row FAILED, the way a failed provision does.
func (s *ProvisioningService) failProjectWithoutDatabase(ctx context.Context, inst *domain.DatabaseInstance, step string, cause error) *domain.ProvisioningResponse {
	log.Printf("project %s without a database failed at %s: %v", inst.ProjectID, step, cause)
	if err := s.k8sClient.DeleteNamespace(context.WithoutCancel(ctx), inst.Namespace); err != nil {
		log.Printf("WARN: remove namespace %s of failed project: %v", inst.Namespace, err)
	}
	inst.Status = string(domain.StageFailed)
	inst.CurrentStage = domain.StageFailed
	inst.FailureStage = domain.StageNamespaceCreation
	inst.FailureStep = step
	inst.FailureReason = cause.Error()
	return s.saveProvisionFailure(inst)
}

func projectResponse(inst *domain.DatabaseInstance) *domain.ProvisioningResponse {
	return &domain.ProvisioningResponse{
		ProjectID:     inst.ProjectID,
		ProjectName:   inst.ProjectName,
		Status:        inst.Status,
		CurrentStage:  inst.CurrentStage,
		Namespace:     inst.Namespace,
		Host:          inst.Host,
		Port:          inst.Port,
		DatabaseName:  inst.DatabaseName,
		FailureReason: inst.FailureReason,
		FailureStage:  inst.FailureStage,
		FailureStep:   inst.FailureStep,
		RollbackLog:   inst.RollbackLog,
		CreatedAt:     inst.CreatedAt,
		NoDatabase:    inst.NoDatabase,
	}
}

// AddDatabase gives a project created without a database its database. The
// request carries only database settings; the project's plan sizes it, and
// it is admitted, built and registered exactly as a project's database is at
// creation. The project already holds its org slot, so none is taken.
//
// A build that fails is rolled back resource by resource inside the
// project's namespace, and the project stays ACTIVE without a database, with
// the failure recorded, so its apps keep running and the add can be retried.
func (s *ProvisioningService) AddDatabase(ctx context.Context, projectID string, req domain.ProvisioningRequest) (*domain.ProvisioningResponse, error) {
	if req.NoDatabase || req.ProjectName != "" || req.OrgID != "" {
		return nil, ErrAddDatabaseRequest
	}
	if err := validateDatabaseRequest(req); err != nil {
		return nil, err
	}
	databases, ok := s.store.(storage.ProjectDatabaseStore)
	if !ok {
		return nil, errors.New("this platform's store cannot add a database to a project")
	}
	inst, release, err := s.holdProject(ctx, projectID, OperationAddDatabase, requireActive)
	if err != nil {
		return nil, err
	}
	defer release()
	if !inst.NoDatabase {
		return nil, fmt.Errorf("%w: %s", storage.ErrProjectHasDatabase, projectID)
	}
	if inst.DeploymentMode != domain.ModeK8s {
		return nil, ErrNoDatabaseNeedsKubernetes
	}

	start := time.Now()
	tier, err := s.databaseTier(ctx, &req, inst.Tier)
	if err != nil {
		return nil, err
	}
	prov, major, err := s.admitDatabase(ctx, &req, inst.Tier, tier, projectID)
	if err != nil {
		return nil, err
	}
	applyDatabaseChoices(inst, req, tier, major)
	if err := databases.RecordDatabaseChoices(inst, string(domain.StatusActive)); err != nil {
		return nil, err
	}
	inst.Status = string(domain.StatusProvisioning)
	inst.CurrentStage = domain.StageValidating
	inst.CurrentStep = ""
	inst.FailureReason, inst.FailureStage, inst.FailureStep, inst.RollbackLog = "", "", "", ""
	if err := s.store.UpdateIfStatus(inst, string(domain.StatusActive)); err != nil {
		return nil, err
	}

	req.ProjectName = inst.ProjectID
	req.OrgID = inst.OrgID
	req.IntoExistingNamespace = true
	return s.buildDatabase(ctx, start, inst, req, prov, tier, s.handleAddDatabaseFailure,
		RegistrationOptions{AddingDatabase: true})
}

// handleAddDatabaseFailure rolls back what the add built and returns the
// project to ACTIVE without a database, the failure kept on the row.
func (s *ProvisioningService) handleAddDatabaseFailure(ctx context.Context, inst *domain.DatabaseInstance,
	req domain.ProvisioningRequest, err error, pc *provisioner.ProvisionContext) *domain.ProvisioningResponse {
	s.recordProvisionFailure(ctx, inst, req, err, pc)
	inst.Status = string(domain.StatusActive)
	inst.CurrentStage = domain.StageCompleted
	inst.CurrentStep = ""
	inst.NoDatabase = true
	inst.Host, inst.ReadOnlyHost, inst.DatabaseName, inst.Username, inst.Password, inst.SSLMode = "", "", "", "", "", ""
	inst.Port = nil
	inst.MetricsEndpoint = ""
	// No database, so nothing is backed up: the backup credential renewal
	// reads this flag and must not look for a secret the rollback removed.
	inst.BackupEnabled, inst.BackupSchedule, inst.BackupRetentionDays = nil, "", nil
	return s.saveProvisionFailure(inst)
}
