package handler

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/tableimport"
)

// projectImportTarget loads into a project through the schema browser's
// pools: the same excalibase_app login and session timeouts as Studio's SQL
// runner.
type projectImportTarget struct {
	pools     projectPools
	instances storage.InstanceStore
	loader    tableimport.Loader
}

// NewImportTarget shares this handler's pools with the table import.
func (h *SchemaHandler) NewImportTarget(loader tableimport.Loader) ImportTarget {
	return &projectImportTarget{pools: h.pools, instances: h.instances, loader: loader}
}

func (t *projectImportTarget) Load(ctx context.Context, projectID string, opts tableimport.Options, src tableimport.Source, estimatedBytes int64) (tableimport.Result, error) {
	if !isValidID(projectID) {
		return tableimport.Result{}, badRequest("invalid project id")
	}
	db, err := t.pools.Open(ctx, projectID)
	if err != nil {
		return tableimport.Result{}, fmt.Errorf("open project database: %w", err)
	}
	if err := t.checkDisk(ctx, projectID, db, estimatedBytes); err != nil {
		return tableimport.Result{}, err
	}
	return t.loader.Load(ctx, db, opts, src)
}

// checkDisk refuses before writing when the import would fill the disk; a
// full Postgres volume stops every write the project makes, not only this one.
func (t *projectImportTarget) checkDisk(ctx context.Context, projectID string, db *sql.DB, estimatedBytes int64) error {
	if t.instances == nil {
		return nil
	}
	inst, err := t.instances.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return fmt.Errorf("find project %s: %w", projectID, err)
	}
	disk := diskBytes(inst.StorageSize)
	if disk == 0 {
		log.Printf("WARN: import into %s: the project has no recorded disk size; relying on Postgres to refuse a full disk", safeLog(projectID))
		return nil
	}
	var used int64
	if err := db.QueryRowContext(ctx, "SELECT pg_database_size(current_database())").Scan(&used); err != nil {
		return fmt.Errorf("read database size: %w", err)
	}
	return tableimport.CheckDiskHeadroom(used, disk, estimatedBytes)
}

// diskBytes reads a Kubernetes quantity such as "5Gi"; 0 when unset or unreadable.
func diskBytes(size string) int64 {
	if size == "" {
		return 0
	}
	quantity, err := resource.ParseQuantity(size)
	if err != nil {
		return 0
	}
	return quantity.Value()
}
