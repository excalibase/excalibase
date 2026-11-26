package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const (
	errProjectNotFoundFmt = "project not found: %s"
	primaryPodSuffix      = "-postgres-1"
	auditLogSetting       = k8s.AuditLogParameter
	// defaultAuditClasses is what an enable that names none records.
	defaultAuditClasses = "ddl,role,write"
	// auditLoadBudget bounds the background wait for the restart that preloads pgaudit.
	auditLoadBudget = 10 * time.Minute
	// auditScanLines is how much of the pod log an audit read searches.
	auditScanLines = 10000
)

// OperationAudit is the lease turning a project's audit on holds.
const OperationAudit ProjectOperation = "audit"

var (
	// ErrAuditClasses refuses a pgaudit.log value that is not a list of pgaudit classes.
	ErrAuditClasses = errors.New("pgaudit.log must be a comma-separated list of: read, write, function, role, ddl, misc, misc_set, all")
	// ErrAuditDisableUnsupported: turning audit off would unload a library the
	// database's event triggers call, so it is not offered.
	ErrAuditDisableUnsupported = errors.New("turning audit off is not supported")
)

var auditClassNames = map[string]bool{
	"read": true, "write": true, "function": true, "role": true, "ddl": true, "misc": true, "misc_set": true, "all": true,
}

// AuditService enables pgaudit and reads its log and settings.
type AuditService struct {
	store     storage.InstanceStore
	k8sClient k8s.KubeClient
	prov      *ProvisioningService
	// pollEvery paces the background wait for the library; finishing lets tests wait for it.
	pollEvery time.Duration
	finishing sync.WaitGroup
}

func NewAuditService(store storage.InstanceStore, client k8s.KubeClient, prov *ProvisioningService) *AuditService {
	return &AuditService{store: store, k8sClient: client, prov: prov, pollEvery: 3 * time.Second}
}

// AuditLogValue is the pgaudit.log setting a request names: the default when it
// names none, otherwise its classes trimmed and joined. Whitespace inside a class,
// an empty class or an unknown one is refused.
func AuditLogValue(config domain.AuditConfig) (string, error) {
	value, named := config.Settings[auditLogSetting]
	if !named {
		return defaultAuditClasses, nil
	}
	if strings.ContainsAny(value, "\r\n\t") {
		return "", ErrAuditClasses
	}
	classes := strings.Split(value, ",")
	for i, class := range classes {
		classes[i] = strings.TrimSpace(class)
		if !auditClassNames[classes[i]] {
			return "", ErrAuditClasses
		}
	}
	return strings.Join(classes, ","), nil
}

// EnableAudit puts a pgaudit.log setting on the cluster, which makes CloudNativePG
// preload the library and restart the database, and returns. A background task
// then waits for the restart and creates the extension, so the call fits the
// edge's timeout and survives the caller going away. Calling it again while the
// setting is already there finishes a task that was lost (EXC-688).
func (s *AuditService) EnableAudit(ctx context.Context, projectID string, config domain.AuditConfig) error {
	if !config.Enabled {
		return ErrAuditDisableUnsupported
	}
	value, err := AuditLogValue(config)
	if err != nil {
		return err
	}
	inst, err := s.prov.setAuditParameter(ctx, projectID, value)
	if err != nil {
		return err
	}
	s.finishing.Add(1)
	go func() {
		defer s.finishing.Done()
		finishCtx, cancel := context.WithTimeout(context.Background(), auditLoadBudget)
		defer cancel()
		if err := s.createExtension(finishCtx, inst); err != nil {
			log.Printf("ERROR: audit for project %s: %v", inst.ProjectID, err)
		}
	}()
	return nil
}

// setAuditParameter holds the project, puts pgaudit.log on its cluster unless it
// is already there, and returns the project.
func (s *ProvisioningService) setAuditParameter(ctx context.Context, projectID, value string) (*domain.DatabaseInstance, error) {
	if err := s.orgGuard.CheckProject(ctx, projectID); err != nil {
		return nil, err
	}
	inst, release, err := s.holdProject(ctx, projectID, OperationAudit, requireActive)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := requirePostgres(inst); err != nil {
		return nil, err
	}
	if err := s.requireKubernetes(inst); err != nil {
		return nil, err
	}
	cluster, err := s.projectCluster(ctx, inst)
	if err != nil {
		return nil, err
	}
	if changed, differs := k8s.WithPostgresParameter(cluster, auditLogSetting, value); differs {
		if err := s.k8sClient.UpdateCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, changed); err != nil {
			return nil, fmt.Errorf("apply %s to the cluster: %w", auditLogSetting, err)
		}
	}
	// The record is what a restore reads: the cluster may be gone by then.
	if inst.Parameters[auditLogSetting] != value {
		recorder, ok := s.store.(storage.ProjectParametersStore)
		if !ok {
			return nil, errors.New("the project store cannot record Postgres settings")
		}
		recorded := maps.Clone(inst.Parameters)
		if recorded == nil {
			recorded = map[string]string{}
		}
		recorded[auditLogSetting] = value
		if err := recorder.UpdateParametersIfStatus(projectID, recorded, inst.Status); err != nil {
			return nil, fmt.Errorf("record %s: %w", auditLogSetting, err)
		}
	}
	return inst, nil
}

// The states GET audit/config reports.
const (
	AuditStatusEnabled  = "enabled"
	AuditStatusPending  = "pending"
	AuditStatusDisabled = "disabled"
)

// auditStatus is enabled only when the library is preloaded and the extension
// exists; the stretch between them, while the restart finishes, is pending.
func (s *AuditService) auditStatus(ctx context.Context, inst *domain.DatabaseInstance, primary string, preloaded bool) string {
	if !preloaded {
		return AuditStatusDisabled
	}
	out, err := s.k8sClient.ExecInPod(ctx, inst.Namespace, primary, postgresContainer,
		auditPSQL("-t", "-A", "-c", "SELECT count(*) FROM pg_extension WHERE extname = 'pgaudit'"))
	if err != nil || strings.TrimSpace(out) != "1" {
		return AuditStatusPending
	}
	return AuditStatusEnabled
}

// auditPSQL is psql as the superuser, with the given flags.
func auditPSQL(flags ...string) []string {
	return append([]string{"psql", "-U", "postgres"}, flags...)
}

// createExtension waits until the primary has pgaudit preloaded, then creates the extension.
func (s *AuditService) createExtension(ctx context.Context, inst *domain.DatabaseInstance) error {
	for {
		if primary, err := s.prov.currentPrimary(ctx, inst); err == nil && s.libraryLoaded(ctx, inst, primary) {
			_, err := s.k8sClient.ExecInPod(ctx, inst.Namespace, primary, postgresContainer,
				auditPSQL("-c", "CREATE EXTENSION IF NOT EXISTS pgaudit"))
			return err
		}
		select {
		case <-ctx.Done():
			return errors.New("pgaudit was not loaded before the wait ended; enable audit again to finish")
		case <-time.After(s.pollEvery):
		}
	}
}

func (s *AuditService) libraryLoaded(ctx context.Context, inst *domain.DatabaseInstance, primary string) bool {
	out, err := s.k8sClient.ExecInPod(ctx, inst.Namespace, primary, postgresContainer,
		auditPSQL("-t", "-A", "-c", "SHOW shared_preload_libraries"))
	return err == nil && strings.Contains(out, "pgaudit")
}

// GetAuditLogs is the last pgaudit lines of the primary's log. CNPG writes
// parsed pgaudit records under the "pgaudit" logger and leaves the others as
// "AUDIT:" messages.
func (s *AuditService) GetAuditLogs(ctx context.Context, projectID string, lines int) (string, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return "", fmt.Errorf(errProjectNotFoundFmt, projectID)
	}
	primary, err := s.prov.currentPrimary(ctx, inst)
	if err != nil {
		return "", err
	}
	podLog, err := s.k8sClient.PodLogTail(ctx, inst.Namespace, primary, postgresContainer, auditScanLines)
	if err != nil {
		return "", err
	}
	var audit []string
	for _, line := range strings.Split(podLog, "\n") {
		if strings.Contains(line, `"logger":"pgaudit"`) || strings.Contains(line, "AUDIT:") {
			audit = append(audit, line)
		}
	}
	if lines > 0 && len(audit) > lines {
		audit = audit[len(audit)-lines:]
	}
	if len(audit) == 0 {
		return "", nil
	}
	return strings.Join(audit, "\n") + "\n", nil
}

func (s *AuditService) GetAuditConfig(ctx context.Context, projectID string) (*domain.AuditConfig, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return nil, fmt.Errorf(errProjectNotFoundFmt, projectID)
	}
	primary, err := s.prov.currentPrimary(ctx, inst)
	if err != nil {
		return &domain.AuditConfig{Enabled: false}, nil
	}
	out, err := s.k8sClient.ExecInPod(ctx, inst.Namespace, primary, postgresContainer,
		auditPSQL("-t", "-A", "-c", "SELECT name, setting FROM pg_settings WHERE name LIKE 'pgaudit.%'"))
	if err != nil {
		return &domain.AuditConfig{Enabled: false}, nil
	}

	settings := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) == 2 {
			settings[parts[0]] = parts[1]
		}
	}

	status := s.auditStatus(ctx, inst, primary, len(settings) > 0)
	return &domain.AuditConfig{
		Enabled:  status == AuditStatusEnabled,
		Status:   status,
		Settings: settings,
	}, nil
}
