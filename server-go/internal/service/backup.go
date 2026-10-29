package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const (
	s3CredsKey     = "backup-s3-creds"
	warnMarshalFmt = "WARN: marshal backup record %s: %v"
	warnWriteFmt   = "WARN: write %s: %v"
)

// BackupService is the entry point all HTTP handlers call. It owns
// nothing mode-specific — every Trigger/List/Restore call resolves the
// adapter for the project's DeploymentMode and forwards. CNPG, Docker,
// and (future) MySQL adapters all live behind this dispatch.
type BackupService struct {
	store    storage.InstanceStore
	adapters map[domain.DeploymentMode]BackupAdapter
	// engineAdapters serve engines other than Postgres (backup_dispatch.go).
	engineAdapters map[engineAdapterKey]BackupAdapter
	// capacity meters a restore against the source organisation's tier
	// limit. A restore creates a project, so it is admitted on the same
	// terms as a provision.
	capacity OrgProjectCapacity
	// inPlace replaces a project's database from its own backups; nil on a
	// platform wired without it.
	inPlace *InPlaceRestore
	// archives says which major a restore point was written on (EXC-668).
	archives *MajorUpgradeArchives
}

// ErrRestorePointBeforeMajorUpgrade refuses an in-place restore to a point on
// a major the project has since been upgraded from: it would put the old
// major back under the new one. A copy into a new project can reach it.
var ErrRestorePointBeforeMajorUpgrade = errors.New("that restore point is from before this project's last major upgrade; restore it into a new project instead")

// backupMajorer is an adapter that knows which major a backup was taken on.
type backupMajorer interface {
	BackupMajor(ctx context.Context, inst *domain.DatabaseInstance, backupID string) (string, error)
}

// archiveAware is an adapter that reads each major's archive.
type archiveAware interface {
	SetMajorUpgradeArchives(*MajorUpgradeArchives)
}

// SetMajorUpgradeArchives wires which archive each major of an upgraded
// project uses, here and in every adapter that reads archives.
func (s *BackupService) SetMajorUpgradeArchives(archives *MajorUpgradeArchives) {
	s.archives = archives
	for _, adapter := range s.adapters {
		if aware, ok := adapter.(archiveAware); ok {
			aware.SetMajorUpgradeArchives(archives)
		}
	}
}

// restorePoint is inst as a restore of req must see it: on the major req's
// point was written on.
func (s *BackupService) restorePoint(ctx context.Context, inst *domain.DatabaseInstance, adapter BackupAdapter,
	req domain.RestoreRequest) (*domain.DatabaseInstance, domain.RestoreRequest, error) {
	var majorOf BackupMajorReader
	if reader, ok := adapter.(backupMajorer); ok {
		majorOf = reader.BackupMajor
	}
	return s.archives.RestorePoint(ctx, inst, req, majorOf)
}

// CheckInPlaceRestorePoint refuses, before a job is filed, an in-place
// restore to a point the project has been upgraded past.
func (s *BackupService) CheckInPlaceRestorePoint(ctx context.Context, projectID string, req domain.RestoreRequest) error {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return fmt.Errorf("project not found: %s", projectID)
	}
	adapter, err := s.adapterFor(inst)
	if err != nil {
		return err
	}
	view, _, err := s.restorePoint(ctx, inst, adapter, req)
	if err != nil {
		return err
	}
	if view.PostgresVersion != inst.PostgresVersion {
		return ErrRestorePointBeforeMajorUpgrade
	}
	return nil
}

// SetInPlaceRestore wires restores that replace a project's own database.
func (s *BackupService) SetInPlaceRestore(r *InPlaceRestore) { s.inPlace = r }

// SupportsInPlaceRestore reports whether projects deployed in mode can have
// their database replaced from their backups.
func (s *BackupService) SupportsInPlaceRestore(mode domain.DeploymentMode) bool {
	if mode == "" {
		mode = domain.ModeK8s
	}
	_, ok := s.adapters[mode].(InPlaceCluster)
	return ok
}

// RestoreInPlace replaces projectID's database with its own backups at the
// point req names. The project keeps its id, so no project slot is needed.
func (s *BackupService) RestoreInPlace(ctx context.Context, projectID string, req domain.RestoreRequest) error {
	if s.inPlace == nil {
		return ErrInPlaceRestoreNotConfigured
	}
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return fmt.Errorf("project not found: %s", projectID)
	}
	adapter, err := s.adapterFor(inst)
	if err != nil {
		return err
	}
	cluster, ok := adapter.(InPlaceCluster)
	if !ok {
		return ErrInPlaceRestoreUnsupported
	}
	if err := s.CheckInPlaceRestorePoint(ctx, projectID, req); err != nil {
		return err
	}
	return s.inPlace.Restore(ctx, cluster, projectID, req)
}

// ErrBackupsNotConfigured refuses a backup of a project that has backups off,
// or on a platform with nowhere to write them.
var ErrBackupsNotConfigured = errors.New("backups are not configured for this project")

// backupsOn reads the project's backup flag; a row that never set it has none.
func backupsOn(inst *domain.DatabaseInstance) bool {
	return inst.BackupEnabled != nil && *inst.BackupEnabled
}

// ErrOrgCapacityNotConfigured is returned when a restore cannot be metered
// against the organisation's project limit. There is no unmetered path: an
// unanswerable limit fails the restore rather than granting a free project.
var ErrOrgCapacityNotConfigured = errors.New("restore: organisation project limit is not configured")

// NewBackupService keeps the single-K8s wiring used by tests. Internally
// it builds a one-entry adapter map so dispatch still works. backupStorage
// is the store backups are written to; restores read from the same one.
func NewBackupService(store storage.InstanceStore, client k8s.KubeClient, storagePath string, backupStorage BackupStorageSource) *BackupService {
	adapter := NewK8sBackupAdapter(client, backupStorage)
	adapter.SetInstanceStore(store)
	return NewBackupServiceWithAdapters(store, map[domain.DeploymentMode]BackupAdapter{
		domain.ModeK8s: adapter,
	}, storagePath)
}

// NewBackupServiceWithAdapters is the explicit constructor — main.go
// uses it to wire both K8s and Docker adapters when the platform is
// configured for both modes.
func NewBackupServiceWithAdapters(store storage.InstanceStore, adapters map[domain.DeploymentMode]BackupAdapter, _ string) *BackupService {
	return &BackupService{store: store, adapters: adapters}
}

// RegisterAdapter adds or replaces an adapter at runtime. Used when
// the Docker client is wired post-construction (matches the rest of
// the service's setter-style dependency injection).
// SetProjectRegistrar hands the shared registration path to every adapter
// that restores into a new project. Without it a restore refuses to run
// rather than producing a database no API route can reach.
func (s *BackupService) SetProjectRegistrar(r ProjectRegistrar) {
	for _, adapter := range s.everyAdapter() {
		if setter, ok := adapter.(interface{ SetProjectRegistrar(ProjectRegistrar) }); ok {
			setter.SetProjectRegistrar(r)
		}
	}
}

// SetRestorePlanSource hands every adapter that plans a restored project what
// decides its tier and backups.
func (s *BackupService) SetRestorePlanSource(p RestorePlanSource) {
	for _, adapter := range s.everyAdapter() {
		if setter, ok := adapter.(interface{ SetRestorePlanSource(RestorePlanSource) }); ok {
			setter.SetRestorePlanSource(p)
		}
	}
}

// SetBackupCredentials hands every adapter that writes object-store
// credentials into a namespace the issuer that mints them.
func (s *BackupService) SetBackupCredentials(i *BackupCredentialIssuer) {
	for _, adapter := range s.everyAdapter() {
		if setter, ok := adapter.(interface{ SetBackupCredentials(*BackupCredentialIssuer) }); ok {
			setter.SetBackupCredentials(i)
		}
	}
}

// SetDatabaseProbe hands every adapter the check that proves a recovered
// database serves queries before its project is activated.
//
// It reports an error instead of wiring nothing: a platform whose adapters
// cannot verify a restore has no business starting, and finding that out
// from a customer's first restore is finding it out too late.
func (s *BackupService) SetDatabaseProbe(p DatabaseProbe) error {
	if p == nil {
		return fmt.Errorf("%w: no probe was supplied", ErrDatabaseProbeNotConfigured)
	}
	if len(s.everyAdapter()) == 0 {
		return fmt.Errorf("%w: no backup adapters are registered", ErrDatabaseProbeNotConfigured)
	}
	for mode, adapter := range s.everyAdapter() {
		setter, ok := adapter.(interface{ SetDatabaseProbe(DatabaseProbe) })
		if !ok {
			return fmt.Errorf("%w: the %s adapter cannot verify a restore", ErrDatabaseProbeNotConfigured, mode)
		}
		setter.SetDatabaseProbe(p)
	}
	return nil
}

// SetRestoreReadyTimeout bounds how long every adapter waits for a recovered
// database to be observed ready.
func (s *BackupService) SetRestoreReadyTimeout(d time.Duration) {
	for _, adapter := range s.everyAdapter() {
		if setter, ok := adapter.(interface{ SetRestoreReadyTimeout(time.Duration) }); ok {
			setter.SetRestoreReadyTimeout(d)
		}
	}
}

func (s *BackupService) RegisterAdapter(mode domain.DeploymentMode, adapter BackupAdapter) {
	if s.adapters == nil {
		s.adapters = make(map[domain.DeploymentMode]BackupAdapter)
	}
	s.adapters[mode] = adapter
}

func (s *BackupService) TriggerManualBackup(ctx context.Context, projectID string) (map[string]interface{}, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}
	adapter, err := s.adapterFor(inst)
	if err != nil {
		return nil, err
	}
	if !backupsOn(inst) || !adapter.BackupsConfigured() {
		return nil, ErrBackupsNotConfigured
	}
	ref, err := adapter.TriggerManual(ctx, inst)
	if err != nil {
		return nil, err
	}
	return refToMap(ref), nil
}

// K8sPostgres is the Kubernetes Postgres adapter, nil without one.
func (s *BackupService) K8sPostgres() *K8sBackupAdapter {
	adapter, _ := s.adapters[domain.ModeK8s].(*K8sBackupAdapter)
	return adapter
}

// DockerPostgres is the single host's Postgres backup adapter; nil where the
// platform has none.
func (s *BackupService) DockerPostgres() *DockerBackupAdapter {
	adapter, _ := s.adapters[domain.ModeDocker].(*DockerBackupAdapter)
	return adapter
}

// TriggerScheduledBackup is a schedule's backup of a running project. A
// project that is not running (paused, being restored or deleted) is passed
// over: a paused one wrote nothing since its pause backup (EXC-593).
func (s *BackupService) TriggerScheduledBackup(ctx context.Context, projectID string) (map[string]interface{}, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}
	if inst.Status != string(domain.StatusActive) {
		log.Printf("scheduled backup of %s passed over: it is %s", projectID, inst.Status)
		return nil, nil
	}
	return s.TriggerManualBackup(ctx, projectID)
}

func (s *BackupService) ListBackups(projectID string) ([]map[string]interface{}, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return []map[string]interface{}{}, nil
	}
	adapter, err := s.adapterFor(inst)
	if err != nil {
		return nil, err
	}
	refs, err := adapter.List(context.Background(), inst)
	if err != nil {
		return nil, err
	}
	result := make([]map[string]interface{}, 0, len(refs))
	for _, ref := range refs {
		result = append(result, refToMap(ref))
	}
	return result, nil
}

// BackupsConfigured answers, for one project, whether a backup taken now
// would be written anywhere. Two things have to hold: the project has backups
// turned on, and the platform has an object store to put them in.
//
// The per-project half matters as much as the platform half. A project on a
// tier configured without backups that is asked for one anyway gets a Backup CR
// the engine accepts and then fails asynchronously — long after whoever asked
// believed it had a recovery point (EXC-363). A row whose flag was never set
// is treated as off: nothing has claimed this project has backups.
func (s *BackupService) BackupsConfigured(projectID string) (bool, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return false, fmt.Errorf("project not found: %s", projectID)
	}
	if !backupsOn(inst) {
		return false, nil
	}
	adapter, err := s.adapterFor(inst)
	if err != nil {
		return false, err
	}
	return adapter.BackupsConfigured(), nil
}

// LatestBackupID names the project's newest backup by start time, or "" when
// it has none. A pause uses it to tell whether the backup it recorded for
// this episode is still the recovery point a fresh one would produce.
func (s *BackupService) LatestBackupID(ctx context.Context, projectID string) (string, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return "", fmt.Errorf("project not found: %s", projectID)
	}
	adapter, err := s.adapterFor(inst)
	if err != nil {
		return "", err
	}
	refs, err := adapter.List(ctx, inst)
	if err != nil {
		return "", err
	}
	latest := ""
	newest := ""
	for _, ref := range refs {
		if ref.StartedAt > newest || latest == "" {
			newest, latest = ref.StartedAt, ref.ID
		}
	}
	return latest, nil
}

// BackupStatus reports one backup's current status. Listing is what syncs a
// backup record with what the engine says about it, so this is also how a
// caller watching a backup sees it finish.
func (s *BackupService) BackupStatus(ctx context.Context, projectID, backupID string) (string, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return "", fmt.Errorf("project not found: %s", projectID)
	}
	adapter, err := s.adapterFor(inst)
	if err != nil {
		return "", err
	}
	if resolver, ok := adapter.(BackupStatusResolver); ok {
		return resolver.BackupStatusOf(ctx, inst, backupID)
	}
	refs, err := adapter.List(ctx, inst)
	if err != nil {
		return "", err
	}
	for _, ref := range refs {
		if ref.ID == backupID {
			return ref.Status, nil
		}
	}
	return "", fmt.Errorf("backup %s not found for project %s", backupID, projectID)
}

func (s *BackupService) RestoreFromBackup(ctx context.Context, projectID string, req domain.RestoreRequest) (*domain.ProvisioningResponse, error) {
	inst, adapter, req, err := s.copySource(ctx, projectID, req)
	if err != nil {
		return nil, err
	}
	if err := s.EnsureOrgProjectCapacity(ctx, inst); err != nil {
		return nil, err
	}
	return adapter.Restore(ctx, inst, req)
}

// RestoreSubmittedCopy runs the restore of a job that took its organisation's
// slot when it was submitted (EXC-639). Counting slots again would count the
// job against itself; registration still takes the slot atomically.
func (s *BackupService) RestoreSubmittedCopy(ctx context.Context, projectID string, req domain.RestoreRequest) error {
	inst, adapter, req, err := s.copySource(ctx, projectID, req)
	if err != nil {
		return err
	}
	if s.capacity == nil {
		return ErrOrgCapacityNotConfigured
	}
	if err := s.capacity.RequireRestoreDiskFits(ctx, inst); err != nil {
		return err
	}
	_, err = adapter.Restore(ctx, inst, req)
	return err
}

// copySource resolves the source project and its adapter for a restore into a
// new project, and validates the request. The source is returned as the
// restore point sees it: on the major that point was written on (EXC-668).
func (s *BackupService) copySource(ctx context.Context, projectID string, req domain.RestoreRequest) (*domain.DatabaseInstance, BackupAdapter, domain.RestoreRequest, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return nil, nil, req, fmt.Errorf("project not found: %s", projectID)
	}
	adapter, err := s.adapterFor(inst)
	if err != nil {
		return nil, nil, req, err
	}
	if err := req.Validate(); err != nil {
		return nil, nil, req, err
	}
	if req.TargetProjectID == "" {
		return nil, nil, req, ErrTargetProjectIDMissing
	}
	view, adjusted, err := s.restorePoint(ctx, inst, adapter, req)
	if err != nil {
		return nil, nil, req, err
	}
	return view, adapter, adjusted, nil
}

// SetOrgProjectCapacity wires the organisation project limit a restore is
// admitted against. *ProvisioningService supplies it.
func (s *BackupService) SetOrgProjectCapacity(c OrgProjectCapacity) { s.capacity = c }

// EnsureOrgProjectCapacity refuses a restore whose new project would take the
// source project's organisation past its current plan's limit — before the restore
// creates a namespace, a container, or a single vault entry. The handler
// calls it too, so a caller polling an async restore is told at submission
// rather than by a failed job.
func (s *BackupService) EnsureOrgProjectCapacity(ctx context.Context, source *domain.DatabaseInstance) error {
	if s.capacity == nil {
		return ErrOrgCapacityNotConfigured
	}
	if err := s.capacity.EnsureOrgCanTakeProject(ctx, source.OrgID); err != nil {
		return err
	}
	return s.capacity.RequireRestoreDiskFits(ctx, source)
}

// AllocateProjectID reserves the id a restore will register its new project
// under. The caller of a restore never names it — see EXC-415.
func (s *BackupService) AllocateProjectID() (string, error) {
	return allocateProjectID(s.store)
}

func (s *BackupService) GetInstance(projectID string) (*domain.DatabaseInstance, error) {
	return s.store.FindByProjectID(projectID)
}

// refToMap preserves the JSON shape pre-refactor handlers and Playwright
// specs depend on (lower-camelCase keys, no zero-value fields surfaced).
func refToMap(ref BackupRef) map[string]interface{} {
	m := map[string]interface{}{
		"id":        ref.ID,
		"projectId": ref.ProjectID,
		"type":      ref.Type,
		"status":    ref.Status,
	}
	if ref.StartedAt != "" {
		m["timestamp"] = ref.StartedAt
		m["startedAt"] = ref.StartedAt
	}
	if ref.FinishedAt != "" {
		m["finishedAt"] = ref.FinishedAt
	}
	if ref.SizeBytes > 0 {
		m["sizeBytes"] = ref.SizeBytes
	}
	if ref.Error != "" {
		m["error"] = ref.Error
	}
	return m
}
