package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// OperationQueryStats is the lease turning a project's statistics on holds.
const OperationQueryStats ProjectOperation = "query statistics"

// ErrQueryStatsDocumentDB refuses statistics for a single-host DocumentDB
// project, whose documents share the maintenance database the read runs in.
var ErrQueryStatsDocumentDB = errors.New("query statistics are not available for DocumentDB projects on a single host")

// dockerQueryStats turns statistics on in a single host's project container (EXC-673).
type dockerQueryStats interface {
	EnableQueryStats(ctx context.Context, containerID string) error
}

// SetQueryStats is the slowqueries feature for the projects this service creates.
func (s *ProvisioningService) SetQueryStats(on bool) { s.queryStats = on }

// EnableQueryStats turns statement statistics on for a project created
// without them (EXC-654). A Postgres cluster gains the pg_stat_statements
// settings and CloudNativePG restarts it once; MySQL needs only a grant.
func (s *ProvisioningService) EnableQueryStats(ctx context.Context, projectID string) error {
	inst, release, err := s.holdProject(ctx, projectID, OperationQueryStats, requireActive)
	if err != nil {
		return err
	}
	defer release()
	if inst.DBType == domain.MySQL {
		mysql, err := s.mysqlServerOf()
		if err != nil {
			return err
		}
		return mysql.GrantQueryStats(ctx, inst.Namespace, inst.ProjectID)
	}
	if inst.DeploymentMode == domain.ModeDocker {
		return s.enableDockerQueryStats(ctx, inst)
	}
	if err := s.requireKubernetes(inst); err != nil {
		return err
	}
	cluster, err := s.projectCluster(ctx, inst)
	if err != nil {
		return err
	}
	if k8s.ClusterHasQueryStats(cluster) {
		return nil
	}
	if err := s.k8sClient.UpdateCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, k8s.WithQueryStats(cluster)); err != nil {
		return fmt.Errorf("turn on query statistics: %w", err)
	}
	return nil
}

// enableDockerQueryStats restarts a single host's Postgres container once to
// load the library, as CloudNativePG does on Kubernetes.
func (s *ProvisioningService) enableDockerQueryStats(ctx context.Context, inst *domain.DatabaseInstance) error {
	if inst.DocumentDB {
		return ErrQueryStatsDocumentDB
	}
	prov, ok := s.factory.Get(domain.PostgreSQL)
	if !ok {
		return ErrKubernetesOnly
	}
	stats, ok := prov.(dockerQueryStats)
	if !ok {
		return ErrKubernetesOnly
	}
	if err := stats.EnableQueryStats(ctx, inst.Namespace); err != nil {
		return fmt.Errorf("turn on query statistics: %w", err)
	}
	return nil
}

// primaryPsql runs psql as the superuser on the project's current primary.
func (s *ProvisioningService) primaryPsql(ctx context.Context, inst *domain.DatabaseInstance) (roleSQLExecer, error) {
	if s.k8sClient == nil && s.dockerClient == nil {
		return nil, errors.New("no database runtime configured")
	}
	primary, err := s.currentPrimary(ctx, inst)
	if err != nil {
		return nil, err
	}
	return s.roleSQLExecer(inst.Namespace, primary), nil
}
