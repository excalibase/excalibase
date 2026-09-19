package postgres

import (
	"database/sql"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func (s *Store) Save(inst *domain.DatabaseInstance) error {
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
			network_policy_enabled,
			maintenance_window, maintenance_window_duration_min, auto_minor_version_upgrade,
			backup_enabled, backup_schedule, backup_retention_days,
			metrics_endpoint, grafana_dashboard_url,
			restored_from_project_id, restored_from_backup_id,
			last_active_at, last_xact_count, pause_reason,
			created_at, updated_at, last_health_check
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40,$41,$42,$43,$44,$45)
		ON CONFLICT (project_id) DO UPDATE SET
			project_name = EXCLUDED.project_name,
			org_id = EXCLUDED.org_id,
			owner_id = EXCLUDED.owner_id,
			database_type = EXCLUDED.database_type,
			tier = EXCLUDED.tier,
			namespace = EXCLUDED.namespace,
			deployment_mode = EXCLUDED.deployment_mode,
			host = EXCLUDED.host,
			read_only_host = EXCLUDED.read_only_host,
			port = EXCLUDED.port,
			database_name = EXCLUDED.database_name,
			username = EXCLUDED.username,
			password = EXCLUDED.password,
			deletion_protection = EXCLUDED.deletion_protection,
			pooler_enabled = EXCLUDED.pooler_enabled,
			pooler_host = EXCLUDED.pooler_host,
			ssl_mode = EXCLUDED.ssl_mode,
			webhook_url = EXCLUDED.webhook_url,
			postgres_version = EXCLUDED.postgres_version,
			tags = EXCLUDED.tags,
			status = EXCLUDED.status,
			current_stage = EXCLUDED.current_stage,
			current_step = EXCLUDED.current_step,
			failure_reason = EXCLUDED.failure_reason,
			failure_stage = EXCLUDED.failure_stage,
			failure_step = EXCLUDED.failure_step,
			rollback_log = EXCLUDED.rollback_log,
			network_policy_enabled = EXCLUDED.network_policy_enabled,
			maintenance_window = EXCLUDED.maintenance_window,
			maintenance_window_duration_min = EXCLUDED.maintenance_window_duration_min,
			auto_minor_version_upgrade = EXCLUDED.auto_minor_version_upgrade,
			backup_enabled = EXCLUDED.backup_enabled,
			backup_schedule = EXCLUDED.backup_schedule,
			backup_retention_days = EXCLUDED.backup_retention_days,
			metrics_endpoint = EXCLUDED.metrics_endpoint,
			grafana_dashboard_url = EXCLUDED.grafana_dashboard_url,
			restored_from_project_id = EXCLUDED.restored_from_project_id,
			restored_from_backup_id = EXCLUDED.restored_from_backup_id,
			last_active_at = EXCLUDED.last_active_at,
			last_xact_count = EXCLUDED.last_xact_count,
			pause_reason = EXCLUDED.pause_reason,
			created_at = EXCLUDED.created_at,
			updated_at = EXCLUDED.updated_at,
			last_health_check = EXCLUDED.last_health_check`,
		inst.ProjectID, inst.ProjectName, inst.OrgID, inst.OwnerID, inst.DBType, inst.Tier, inst.Namespace,
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
		flexTimePtr(inst.CreatedAt), flexTimePtr(inst.UpdatedAt), flexTimePtr(inst.LastHealthCheck),
	)
	return err
}

const pgInstanceColumns = `
	project_id, project_name, org_id, owner_id, database_type, tier, namespace,
	deployment_mode,
	host, read_only_host, port, database_name, username, password,
	deletion_protection, pooler_enabled, pooler_host, ssl_mode,
	webhook_url, postgres_version, tags,
	status, current_stage, current_step, failure_reason, failure_stage, failure_step, rollback_log,
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

func (s *Store) Delete(projectID string) error {
	_, err := s.db.Exec(`DELETE FROM database_instances WHERE project_id = $1`, projectID)
	return err
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
