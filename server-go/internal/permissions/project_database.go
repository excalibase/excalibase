package permissions

import (
	"context"
	"database/sql"

	"github.com/excalibase/provisioning-poc/internal/schema"
)

// ProjectPools opens a project's database (projectdb.Opener).
type ProjectPools interface {
	Open(ctx context.Context, projectID string) (*sql.DB, error)
}

// ProjectDatabase reads a project's live schema through its pooled connection.
type ProjectDatabase struct {
	pools        ProjectPools
	introspector *schema.Introspector
}

func NewProjectDatabase(pools ProjectPools, introspector *schema.Introspector) *ProjectDatabase {
	return &ProjectDatabase{pools: pools, introspector: introspector}
}

// FunctionDetails returns every routine with that name ("" schema = any).
func (d *ProjectDatabase) FunctionDetails(ctx context.Context, projectID, schemaName, name string) ([]schema.FunctionDetail, error) {
	db, err := d.pools.Open(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return d.introspector.FunctionDetails(ctx, db, schemaName, name)
}
