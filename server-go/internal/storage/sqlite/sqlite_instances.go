package sqlite

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
		INSERT OR REPLACE INTO database_instances (
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
			created_at, updated_at, last_health_check
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		inst.ProjectID, inst.ProjectName, inst.OrgID, inst.OwnerID, inst.DBType, inst.Tier, inst.Namespace,
		mode,
		inst.Host, inst.ReadOnlyHost, inst.Port, inst.DatabaseName, inst.Username, inst.Password,
		boolToInt(inst.DeletionProtection), boolToInt(inst.PoolerEnabled), inst.PoolerHost, inst.SSLMode,
		inst.WebhookURL, inst.PostgresVersion, inst.Tags,
		inst.Status, inst.CurrentStage, inst.CurrentStep, inst.FailureReason, inst.FailureStage, inst.FailureStep, inst.RollbackLog,
		boolToInt(inst.NetworkPolicyEnabled),
		inst.MaintenanceWindow, inst.MaintenanceWindowDurationMinutes, boolToInt(inst.AutoMinorVersionUpgrade),
		boolToInt(inst.BackupEnabled), inst.BackupSchedule, inst.BackupRetentionDays,
		inst.MetricsEndpoint, inst.GrafanaDashboardURL,
		flexTimeStr(inst.CreatedAt), flexTimeStr(inst.UpdatedAt), flexTimeStr(inst.LastHealthCheck),
	)
	return err
}

const sqliteInstanceColumns = `
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
	created_at, updated_at, last_health_check`

func (s *Store) FindByProjectID(projectID string) (*domain.DatabaseInstance, error) {
	row := s.db.QueryRow(`SELECT`+sqliteInstanceColumns+`
	FROM database_instances WHERE project_id = ?`, projectID)

	inst, err := scanInstanceFrom(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return inst, err
}

func (s *Store) FindAll() ([]*domain.DatabaseInstance, error) {
	rows, err := s.db.Query(`SELECT` + sqliteInstanceColumns + `
	FROM database_instances`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*domain.DatabaseInstance
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
	if result == nil {
		result = make([]*domain.DatabaseInstance, 0)
	}
	return result, nil
}

func (s *Store) Delete(projectID string) error {
	_, err := s.db.Exec(`DELETE FROM database_instances WHERE project_id = ?`, projectID)
	return err
}

func (s *Store) FindByOwner(ownerID string) ([]*domain.DatabaseInstance, error) {
	rows, err := s.db.Query(`SELECT`+sqliteInstanceColumns+`
	FROM database_instances WHERE owner_id = ?`, ownerID)
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

// scanInstanceFrom scans a single instance from any scanner (*sql.Row or *sql.Rows).
func scanInstanceFrom(s scanner) (*domain.DatabaseInstance, error) {
	var inst domain.DatabaseInstance
	var port sql.NullInt64
	var delProt, poolerEn, netPol, autoUpgrade, backupEn sql.NullInt64
	var maintDur, backupRet sql.NullInt64
	var createdAt, updatedAt, lastHealth sql.NullString
	var deployMode sql.NullString

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
		&createdAt, &updatedAt, &lastHealth,
	)
	if err != nil {
		return nil, err
	}

	if deployMode.Valid && deployMode.String != "" {
		inst.DeploymentMode = domain.DeploymentMode(deployMode.String)
	} else {
		inst.DeploymentMode = domain.ModeK8s
	}

	if port.Valid {
		p := int(port.Int64)
		inst.Port = &p
	}
	inst.DeletionProtection = intToBoolPtr(delProt)
	inst.PoolerEnabled = intToBoolPtr(poolerEn)
	inst.NetworkPolicyEnabled = intToBoolPtr(netPol)
	inst.AutoMinorVersionUpgrade = intToBoolPtr(autoUpgrade)
	inst.BackupEnabled = intToBoolPtr(backupEn)
	if maintDur.Valid {
		d := int(maintDur.Int64)
		inst.MaintenanceWindowDurationMinutes = &d
	}
	if backupRet.Valid {
		d := int(backupRet.Int64)
		inst.BackupRetentionDays = &d
	}
	if createdAt.Valid {
		inst.CreatedAt = parseFlexTime(createdAt.String)
	}
	if updatedAt.Valid {
		inst.UpdatedAt = parseFlexTime(updatedAt.String)
	}
	if lastHealth.Valid {
		inst.LastHealthCheck = parseFlexTime(lastHealth.String)
	}

	return &inst, nil
}
