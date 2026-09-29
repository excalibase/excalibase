package permissions

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/schema"
	"github.com/excalibase/provisioning-poc/internal/storage"
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

// TableColumns returns the project's relations with their columns.
func (d *ProjectDatabase) TableColumns(ctx context.Context, projectID string) (map[string][]string, error) {
	db, err := d.pools.Open(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return d.introspector.TableColumns(ctx, db)
}

// LiveReader is what folding needs from a project database.
type LiveReader interface {
	TableColumns(ctx context.Context, projectID string) (map[string][]string, error)
	FunctionDetails(ctx context.Context, projectID, schemaName, name string) ([]schema.FunctionDetail, error)
}

// ChangePublisher announces a project's permissions changed.
type ChangePublisher interface {
	PublishPolicyChange(ctx context.Context, evt domain.PolicyChangeEvent)
}

// LegacyMigrator folds each project's table grants, row policies and column
// policies into permissions once. It runs at startup; a project whose
// database cannot be read is left for the next start. The legacy stores are
// only read, never changed.
type LegacyMigrator struct {
	store     storage.PermissionStore
	grants    storage.TableGrantStore
	policies  storage.RlsPolicyStore
	live      LiveReader
	publisher ChangePublisher
}

func NewLegacyMigrator(store storage.PermissionStore, grants storage.TableGrantStore, policies storage.RlsPolicyStore,
	live LiveReader, publisher ChangePublisher) *LegacyMigrator {
	return &LegacyMigrator{store: store, grants: grants, policies: policies, live: live, publisher: publisher}
}

// Run migrates every pending project and reports how many it migrated. An
// error is returned only when the pending list itself cannot be read.
func (m *LegacyMigrator) Run(ctx context.Context) (int, error) {
	pending, err := m.store.LegacyPending(ctx)
	if err != nil {
		return 0, fmt.Errorf("list projects with legacy policies: %w", err)
	}
	migrated := 0
	for _, projectID := range pending {
		if err := m.migrateProject(ctx, projectID); err != nil {
			log.Printf("permissions: legacy policies of %s not migrated, retrying at the next start: %v", projectID, err)
			continue
		}
		migrated++
	}
	return migrated, nil
}

func (m *LegacyMigrator) migrateProject(ctx context.Context, projectID string) error {
	legacy, err := m.readLegacy(ctx, projectID)
	if err != nil {
		return err
	}
	live, err := m.readLive(ctx, projectID, legacy.Grants)
	if err != nil {
		return err
	}
	result := Fold(legacy, live)
	for _, line := range result.Dropped {
		log.Printf("permissions: %s: %s", projectID, line)
	}
	if err := m.store.ImportLegacy(ctx, projectID, result.Import); err != nil {
		return fmt.Errorf("write permissions: %w", err)
	}
	log.Printf("permissions: %s: legacy policies folded into %d permissions, %d tracked functions, %d not carried over",
		projectID, len(result.Import.Permissions), len(result.Import.Functions), len(result.Dropped))
	if m.publisher != nil {
		m.publisher.PublishPolicyChange(ctx, domain.PolicyChangeEvent{
			ProjectID: projectID, Kind: domain.PermissionChangeKind, Op: domain.OpChangeUpdate,
		})
	}
	return nil
}

func (m *LegacyMigrator) readLegacy(ctx context.Context, projectID string) (LegacyData, error) {
	grants, err := m.grants.ListGrants(ctx, projectID)
	if err != nil {
		return LegacyData{}, fmt.Errorf("read table grants: %w", err)
	}
	policies, err := m.policies.ListRls(ctx, projectID, "")
	if err != nil {
		return LegacyData{}, fmt.Errorf("read row policies: %w", err)
	}
	columns, err := m.policies.ListColumn(ctx, projectID, "")
	if err != nil {
		return LegacyData{}, fmt.Errorf("read column policies: %w", err)
	}
	return LegacyData{Grants: grants, Policies: policies, ColumnPolicies: columns}, nil
}

// readLive loads every relation, and the functions any grant could name: a
// grant's resource may be a function rather than a table.
func (m *LegacyMigrator) readLive(ctx context.Context, projectID string, grants []domain.TableGrant) (LiveSchema, error) {
	tables, err := m.live.TableColumns(ctx, projectID)
	if err != nil {
		return LiveSchema{}, fmt.Errorf("read the project's tables: %w", err)
	}
	live := LiveSchema{Tables: tables, Functions: map[string][]schema.FunctionDetail{}}
	seen := map[string]bool{}
	for _, g := range grants {
		if !g.Enabled || seen[g.Resource] {
			continue
		}
		seen[g.Resource] = true
		schemaName, name, qualified := strings.Cut(g.Resource, ".")
		if !qualified {
			schemaName, name = "", g.Resource
		}
		details, err := m.live.FunctionDetails(ctx, projectID, schemaName, name)
		if err != nil {
			return LiveSchema{}, fmt.Errorf("read functions named %s: %w", g.Resource, err)
		}
		for _, detail := range details {
			key := detail.Schema + "." + detail.Name
			live.Functions[key] = append(live.Functions[key], detail)
		}
	}
	return live, nil
}
