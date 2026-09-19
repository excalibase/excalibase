package postgres

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/lib/pq"
)

// uniqueViolation is the SQLSTATE Postgres raises when an INSERT hits a unique
// or primary-key constraint.
const uniqueViolation = "23505"

// Create registers a new project. A project id already in the table is a
// conflict, never an overwrite: the row belongs to whoever registered it and
// repointing it would move a live tenant's database to another org.
func (s *Store) Create(inst *domain.DatabaseInstance) error {
	mode := inst.DeploymentMode
	if mode == "" {
		mode = domain.ModeK8s
	}
	_, err := s.db.Exec(`
		INSERT INTO database_instances (
			project_id, project_name, org_id, owner_id, database_type, tier, namespace,
			deployment_mode,
			host, read_only_host, port, database_name, username, password,
			deletion_protection, pooler_enabled, pooler_host, ssl_mode,
			webhook_url, postgres_version, tags,
			status, current_stage, current_step, failure_reason, failure_stage, failure_step, rollback_log,
			deletion_step, deletion_error, deletion_delete_backups,
			network_policy_enabled,
			maintenance_window, maintenance_window_duration_min, auto_minor_version_upgrade,
			backup_enabled, backup_schedule, backup_retention_days,
			metrics_endpoint, grafana_dashboard_url,
			restored_from_project_id, restored_from_backup_id,
			last_active_at, last_xact_count, pause_reason,
			created_at, updated_at, last_health_check
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40,$41,$42,$43,$44,$45,$46,$47,$48)`,
		inst.ProjectID, inst.ProjectName, inst.OrgID, inst.OwnerID, inst.DBType, inst.Tier, inst.Namespace,
		mode,
		inst.Host, inst.ReadOnlyHost, inst.Port, inst.DatabaseName, inst.Username, inst.Password,
		derefBool(inst.DeletionProtection), derefBool(inst.PoolerEnabled), inst.PoolerHost, inst.SSLMode,
		inst.WebhookURL, inst.PostgresVersion, inst.Tags,
		inst.Status, inst.CurrentStage, inst.CurrentStep, inst.FailureReason, inst.FailureStage, inst.FailureStep, inst.RollbackLog,
		inst.DeletionStep, inst.DeletionError, inst.DeletionDeleteBackups,
		derefBool(inst.NetworkPolicyEnabled),
		inst.MaintenanceWindow, inst.MaintenanceWindowDurationMinutes, derefBool(inst.AutoMinorVersionUpgrade),
		derefBool(inst.BackupEnabled), inst.BackupSchedule, inst.BackupRetentionDays,
		inst.MetricsEndpoint, inst.GrafanaDashboardURL,
		inst.RestoredFromProjectID, inst.RestoredFromBackupID,
		flexTimePtr(inst.LastActiveAt), inst.LastXactCount, inst.PauseReason,
		flexTimePtr(inst.CreatedAt), flexTimePtr(inst.UpdatedAt), flexTimePtr(inst.LastHealthCheck),
	)
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && string(pqErr.Code) == uniqueViolation {
		return storage.ErrProjectExists
	}
	return err
}

// Update persists changes to an existing project. project_id and org_id are
// absent from the SET list on purpose: a project's identity and its owning org
// are fixed at creation, so no update path can move a tenant's database.
func (s *Store) Update(inst *domain.DatabaseInstance) error {
	mode := inst.DeploymentMode
	if mode == "" {
		mode = domain.ModeK8s
	}
	res, err := s.db.Exec(`
		UPDATE database_instances SET
			project_name = $2,
			owner_id = $3,
			database_type = $4,
			tier = $5,
			namespace = $6,
			deployment_mode = $7,
			host = $8,
			read_only_host = $9,
			port = $10,
			database_name = $11,
			username = $12,
			password = $13,
			deletion_protection = $14,
			pooler_enabled = $15,
			pooler_host = $16,
			ssl_mode = $17,
			webhook_url = $18,
			postgres_version = $19,
			tags = $20,
			status = $21,
			current_stage = $22,
			current_step = $23,
			failure_reason = $24,
			failure_stage = $25,
			failure_step = $26,
			rollback_log = $27,
			network_policy_enabled = $28,
			maintenance_window = $29,
			maintenance_window_duration_min = $30,
			auto_minor_version_upgrade = $31,
			backup_enabled = $32,
			backup_schedule = $33,
			backup_retention_days = $34,
			metrics_endpoint = $35,
			grafana_dashboard_url = $36,
			restored_from_project_id = $37,
			restored_from_backup_id = $38,
			last_active_at = $39,
			last_xact_count = $40,
			pause_reason = $41,
			updated_at = $42,
			last_health_check = $43,
			deletion_step = $44,
			deletion_error = $45
		WHERE project_id = $1 AND status <> ALL($46)`,
		inst.ProjectID, inst.ProjectName, inst.OwnerID, inst.DBType, inst.Tier, inst.Namespace,
		mode,
		inst.Host, inst.ReadOnlyHost, inst.Port, inst.DatabaseName, inst.Username, inst.Password,
		derefBool(inst.DeletionProtection), derefBool(inst.PoolerEnabled), inst.PoolerHost, inst.SSLMode,
		inst.WebhookURL, inst.PostgresVersion, inst.Tags,
		inst.Status, inst.CurrentStage, inst.CurrentStep, inst.FailureReason, inst.FailureStage, inst.FailureStep, inst.RollbackLog,
		derefBool(inst.NetworkPolicyEnabled),
		inst.MaintenanceWindow, inst.MaintenanceWindowDurationMinutes, derefBool(inst.AutoMinorVersionUpgrade),
		derefBool(inst.BackupEnabled), inst.BackupSchedule, inst.BackupRetentionDays,
		inst.MetricsEndpoint, inst.GrafanaDashboardURL,
		inst.RestoredFromProjectID, inst.RestoredFromBackupID,
		flexTimePtr(inst.LastActiveAt), inst.LastXactCount, inst.PauseReason,
		flexTimePtr(inst.UpdatedAt), flexTimePtr(inst.LastHealthCheck),
		inst.DeletionStep, inst.DeletionError,
		pq.Array(deletionStatuses),
	)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return s.explainRefusedUpdate(inst.ProjectID)
	}
	return nil
}

// deletionStatuses are the statuses a general Update may not write over. The
// predicate lives in the UPDATE itself so the door holds across control-plane
// replicas and across any caller, not only the ones that remember to check.
var deletionStatuses = []string{string(domain.StatusDeleting), string(domain.StatusBackupsPendingDelete)}

// explainRefusedUpdate turns "no rows matched" into the reason: either the row
// is gone, or a teardown owns it.
func (s *Store) explainRefusedUpdate(projectID string) error {
	var status string
	err := s.db.QueryRow(`SELECT status FROM database_instances WHERE project_id = $1`, projectID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.ErrProjectNotFound
	}
	if err != nil {
		return fmt.Errorf("read project status: %w", err)
	}
	if domain.IsDeletionStatus(status) {
		return fmt.Errorf("%w: %s", storage.ErrProjectDeleting, projectID)
	}
	return storage.ErrProjectNotFound
}

// BeginDeletion claims the project for teardown in one conditional write. See
// storage.InstanceStore.
func (s *Store) BeginDeletion(projectID string, deleteBackups *bool) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("begin deletion claim: %w", err)
	}
	defer tx.Rollback()

	var status string
	var recorded bool
	var lastMoved time.Time
	err = tx.QueryRow(`
		SELECT status, deletion_delete_backups, COALESCE(updated_at, created_at, to_timestamp(0))
		FROM database_instances WHERE project_id = $1 FOR UPDATE`,
		projectID).Scan(&status, &recorded, &lastMoved)
	if errors.Is(err, sql.ErrNoRows) {
		return false, storage.ErrProjectNotFound
	}
	if err != nil {
		return false, fmt.Errorf("lock project row: %w", err)
	}
	if err := storage.CheckNotBuilding(projectID, status, lastMoved, time.Now()); err != nil {
		return false, err
	}

	effective, err := effectiveBackupIntent(projectID, status, recorded, deleteBackups)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(`
		UPDATE database_instances
		SET status = $2, current_stage = $2, deletion_step = '', deletion_error = '',
		    deletion_delete_backups = $3, updated_at = NOW()
		WHERE project_id = $1`,
		projectID, string(domain.StatusDeleting), effective); err != nil {
		return false, fmt.Errorf("claim project for deletion: %w", err)
	}
	return effective, tx.Commit()
}

// effectiveBackupIntent resolves the backup decision now in force from what
// the row records and what this caller asked for.
func effectiveBackupIntent(projectID, status string, recorded bool, requested *bool) (bool, error) {
	switch {
	case requested == nil:
		return recorded, nil
	case *requested:
		return true, nil
	case recorded && domain.IsDeletionStatus(status):
		return false, fmt.Errorf("%w: %s", storage.ErrBackupPurgeAlreadyConfirmed, projectID)
	default:
		return false, nil
	}
}

// RecordDeletionFailure stores how far a teardown got. See
// storage.InstanceStore.
func (s *Store) RecordDeletionFailure(projectID string, status domain.ProvisioningStage, step, reason string) error {
	res, err := s.db.Exec(`
		UPDATE database_instances
		SET status = $2, current_stage = $2, deletion_step = $3, deletion_error = $4,
		    failure_reason = CASE WHEN $2 = $5 THEN $4 ELSE failure_reason END,
		    updated_at = NOW()
		WHERE project_id = $1 AND status = ANY($6)`,
		projectID, string(status), step, reason,
		string(domain.StatusBackupsPendingDelete), pq.Array(deletionStatuses))
	if err != nil {
		return fmt.Errorf("record deletion failure: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("%w: %s", storage.ErrProjectNotDeleting, projectID)
	}
	return nil
}

const pgInstanceColumns = `
	project_id, project_name, org_id, owner_id, database_type, tier, namespace,
	deployment_mode,
	host, read_only_host, port, database_name, username, password,
	deletion_protection, pooler_enabled, pooler_host, ssl_mode,
	webhook_url, postgres_version, tags,
	status, current_stage, current_step, failure_reason, failure_stage, failure_step, rollback_log,
	deletion_step, deletion_error, deletion_delete_backups,
	network_policy_enabled,
	maintenance_window, maintenance_window_duration_min, auto_minor_version_upgrade,
	backup_enabled, backup_schedule, backup_retention_days,
	metrics_endpoint, grafana_dashboard_url,
	restored_from_project_id, restored_from_backup_id,
	last_active_at, last_xact_count, pause_reason,
	created_at, updated_at, last_health_check`

func (s *Store) FindByProjectID(projectID string) (*domain.DatabaseInstance, error) {
	row := s.db.QueryRow(`SELECT`+pgInstanceColumns+`
	FROM database_instances WHERE project_id = $1`, projectID)

	inst, err := scanInstanceFrom(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return inst, err
}

func (s *Store) FindAll() ([]*domain.DatabaseInstance, error) {
	rows, err := s.db.Query(`SELECT` + pgInstanceColumns + `
	FROM database_instances`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*domain.DatabaseInstance, 0)
	for rows.Next() {
		inst, err := scanInstanceFrom(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, inst)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}
	return result, nil
}

// projectOwnedTables hold a project's configuration, grants and credentials.
// They are keyed by project_id without a foreign key, so removing the
// project row alone would leave them behind — policies and grants naming a
// project id that a later project could be issued. Delete clears them in the
// same transaction as the row, so the project is gone or it is not.
//
// Deliberately excluded: the history tables (database_metrics, alerts,
// backup_records, migration_records, restore_jobs, audit_log), which record
// what happened rather than what the project can do.
var projectOwnedTables = []string{
	"rls_policies",
	"column_policies",
	"table_grants",
	"project_exposure_settings",
	"project_cors_settings",
	"edge_function_settings",
	"edge_functions",
	"edge_shared_files",
	"backup_schedules",
	"nats_credentials",
	"storage_buckets", // storage_objects cascade from these
	"storage_quota",
	"project_members",
	"access_tokens",
}

// Delete removes the project and everything filed under it.
func (s *Store) Delete(projectID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin delete project: %w", err)
	}
	defer tx.Rollback()

	for _, table := range projectOwnedTables {
		// Table names come from the constant list above, never from input.
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE project_id = $1`, projectID); err != nil {
			return fmt.Errorf("delete %s rows: %w", table, err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM database_instances WHERE project_id = $1`, projectID); err != nil {
		return fmt.Errorf("delete project row: %w", err)
	}
	return tx.Commit()
}

func (s *Store) FindByOwner(ownerID string) ([]*domain.DatabaseInstance, error) {
	rows, err := s.db.Query(`SELECT`+pgInstanceColumns+`
	FROM database_instances WHERE owner_id = $1`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*domain.DatabaseInstance, 0)
	for rows.Next() {
		inst, err := scanInstanceFrom(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, inst)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}
	return result, nil
}

func scanInstanceFrom(s scanner) (*domain.DatabaseInstance, error) {
	var inst domain.DatabaseInstance
	var port sql.NullInt64
	var delProt, poolerEn, netPol, autoUpgrade, backupEn sql.NullBool
	var maintDur, backupRet sql.NullInt64
	var createdAt, updatedAt, lastHealth sql.NullTime
	var deployMode sql.NullString
	var lastActiveAt sql.NullTime
	var lastXactCount sql.NullInt64
	var pauseReason sql.NullString

	err := s.Scan(
		&inst.ProjectID, &inst.ProjectName, &inst.OrgID, &inst.OwnerID, &inst.DBType, &inst.Tier, &inst.Namespace,
		&deployMode,
		&inst.Host, &inst.ReadOnlyHost, &port, &inst.DatabaseName, &inst.Username, &inst.Password,
		&delProt, &poolerEn, &inst.PoolerHost, &inst.SSLMode,
		&inst.WebhookURL, &inst.PostgresVersion, &inst.Tags,
		&inst.Status, &inst.CurrentStage, &inst.CurrentStep, &inst.FailureReason, &inst.FailureStage, &inst.FailureStep, &inst.RollbackLog,
		&inst.DeletionStep, &inst.DeletionError, &inst.DeletionDeleteBackups,
		&netPol,
		&inst.MaintenanceWindow, &maintDur, &autoUpgrade,
		&backupEn, &inst.BackupSchedule, &backupRet,
		&inst.MetricsEndpoint, &inst.GrafanaDashboardURL,
		&inst.RestoredFromProjectID, &inst.RestoredFromBackupID,
		&lastActiveAt, &lastXactCount, &pauseReason,
		&createdAt, &updatedAt, &lastHealth,
	)
	if err != nil {
		return nil, err
	}

	nf := nullableInstanceFields{
		deployMode: deployMode, port: port,
		delProt: delProt, poolerEn: poolerEn, netPol: netPol,
		autoUpgrade: autoUpgrade, backupEn: backupEn,
		maintDur: maintDur, backupRet: backupRet,
		createdAt: createdAt, updatedAt: updatedAt, lastHealth: lastHealth,
		lastActiveAt: lastActiveAt, lastXactCount: lastXactCount,
		pauseReason: pauseReason,
	}
	applyNullableInstanceFields(&inst, nf)

	if err := storage.CheckDeploymentMode(inst.ProjectID, inst.DeploymentMode); err != nil {
		return nil, err
	}
	return &inst, nil
}

// nullableInstanceFields groups the sql.Null* values scanned for an instance row
// so applyNullableInstanceFields can map them onto the domain struct in one pass.
type nullableInstanceFields struct {
	deployMode                       sql.NullString
	port, maintDur, backupRet        sql.NullInt64
	delProt, poolerEn, netPol        sql.NullBool
	autoUpgrade, backupEn            sql.NullBool
	createdAt, updatedAt, lastHealth sql.NullTime
	lastActiveAt                     sql.NullTime
	lastXactCount                    sql.NullInt64
	pauseReason                      sql.NullString
}

// applyNullableInstanceFields copies the valid nullable columns onto inst,
// applying the K8s default for an absent deployment mode.
func applyNullableInstanceFields(inst *domain.DatabaseInstance, nf nullableInstanceFields) {
	if nf.deployMode.Valid && nf.deployMode.String != "" {
		inst.DeploymentMode = domain.DeploymentMode(nf.deployMode.String)
	} else {
		inst.DeploymentMode = domain.ModeK8s
	}
	inst.Port = nullIntPtr(nf.port)
	inst.DeletionProtection = nullBoolPtr(nf.delProt)
	inst.PoolerEnabled = nullBoolPtr(nf.poolerEn)
	inst.NetworkPolicyEnabled = nullBoolPtr(nf.netPol)
	inst.AutoMinorVersionUpgrade = nullBoolPtr(nf.autoUpgrade)
	inst.BackupEnabled = nullBoolPtr(nf.backupEn)
	inst.MaintenanceWindowDurationMinutes = nullIntPtr(nf.maintDur)
	inst.BackupRetentionDays = nullIntPtr(nf.backupRet)
	inst.CreatedAt = nullFlexTime(nf.createdAt)
	inst.UpdatedAt = nullFlexTime(nf.updatedAt)
	inst.LastHealthCheck = nullFlexTime(nf.lastHealth)
	inst.LastActiveAt = nullFlexTime(nf.lastActiveAt)
	if nf.lastXactCount.Valid {
		inst.LastXactCount = nf.lastXactCount.Int64
	}
	if nf.pauseReason.Valid {
		inst.PauseReason = nf.pauseReason.String
	}
}

// nullBoolPtr / nullIntPtr / nullFlexTime map sql.Null* columns onto optional
// fields — nil when the column was NULL. They flatten what would otherwise be a
// long if-ladder in applyNullableInstanceFields, keeping its complexity low.
func nullBoolPtr(n sql.NullBool) *bool {
	if n.Valid {
		return boolPtr(n.Bool)
	}
	return nil
}

func nullIntPtr(n sql.NullInt64) *int {
	if n.Valid {
		v := int(n.Int64)
		return &v
	}
	return nil
}

func nullFlexTime(n sql.NullTime) *domain.FlexTime {
	if n.Valid {
		return &domain.FlexTime{Time: n.Time}
	}
	return nil
}
