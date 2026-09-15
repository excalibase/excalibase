package provisioner

import (
	"context"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// StageCallback receives provisioning stage updates (legacy callback form).
type StageCallback func(stage domain.ProvisioningStage)

// DatabaseProvisioner defines the strategy interface for database provisioning.
// Implementations may implement RollbackAware to support compensation on failure.
type DatabaseProvisioner interface {
	Provision(ctx context.Context, req domain.ProvisioningRequest, tier config.TierConfig, cb StageCallback) (*ProvisioningResult, error)
	Deprovision(ctx context.Context, namespace, projectID string) error
	GetStatus(ctx context.Context, namespace, projectID string) (*ProvisioningStatus, error)
	ConfigureBackup(ctx context.Context, namespace, projectID, schedule string, retention int) error
	SupportedType() domain.DatabaseType
}

// Pauser is the optional interface a provisioner implements when it
// supports stop-without-deprovision. K8s/CNPG hibernates the cluster
// (cnpg.io/hibernation annotation); Docker stops the container. Both paths preserve
// data volumes so Resume can spin the workload back up. Adapters
// that lack pause (BYOC) just don't implement this interface and
// the pauseService returns ErrPauseUnsupported.
type Pauser interface {
	Pause(ctx context.Context, namespace, projectID string) error
	Resume(ctx context.Context, namespace, projectID string) error
}

// RollbackAware is an optional interface a provisioner can implement to
// enable per-stage rollback via ProvisionContext. The service layer will
// prefer ProvisionWithRollback when the provisioner implements this.
type RollbackAware interface {
	ProvisionWithRollback(ctx context.Context, req domain.ProvisioningRequest, tier config.TierConfig, pc *ProvisionContext) (*ProvisioningResult, error)
}

type ProvisioningResult struct {
	Host         string
	ReadOnlyHost string
	Port         int
	DatabaseName string
	Username     string
	Password     string
	SSLMode      string
	Namespace    string
}

type ProvisioningStatus struct {
	Phase   string // Running, Pending, Failed
	Ready   bool
	Message string
}

// Factory selects the appropriate provisioner for a database type.
type Factory struct {
	provisioners map[domain.DatabaseType]DatabaseProvisioner
}

func NewFactory(provisionerList ...DatabaseProvisioner) *Factory {
	m := make(map[domain.DatabaseType]DatabaseProvisioner)
	for _, p := range provisionerList {
		m[p.SupportedType()] = p
	}
	return &Factory{provisioners: m}
}

func (f *Factory) Get(dbType domain.DatabaseType) (DatabaseProvisioner, bool) {
	p, ok := f.provisioners[dbType]
	return p, ok
}

// Registered returns every provisioner in the factory. Used by
// optional-feature wiring (Pauser, RollbackAware) to cherry-pick
// implementations that satisfy the optional interface.
func (f *Factory) Registered() []DatabaseProvisioner {
	out := make([]DatabaseProvisioner, 0, len(f.provisioners))
	for _, p := range f.provisioners {
		out = append(out, p)
	}
	return out
}
