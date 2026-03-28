package provisioner

import (
	"context"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// StageCallback receives provisioning stage updates.
type StageCallback func(stage domain.ProvisioningStage)

// DatabaseProvisioner defines the strategy interface for database provisioning.
type DatabaseProvisioner interface {
	Provision(ctx context.Context, req domain.ProvisioningRequest, tier config.TierConfig, cb StageCallback) (*ProvisioningResult, error)
	Deprovision(ctx context.Context, namespace, projectID string) error
	GetStatus(ctx context.Context, namespace, projectID string) (*ProvisioningStatus, error)
	ConfigureBackup(ctx context.Context, namespace, projectID, schedule string, retention int) error
	SupportedType() domain.DatabaseType
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
