package main

import (
	"context"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/tableimport"
)

// newTableImportHandler loads imports through the schema browser's pools,
// under the platform statement and lock timeouts, sized by the org's plan.
func newTableImportHandler(cfg config.AppConfig, schemaHandler *handler.SchemaHandler,
	store storage.InstanceStore, sqlStore storage.PlatformStore) *handler.TableImportHandler {
	loader := tableimport.Loader{
		StatementTimeout: cfg.ProjectDBStatementTimeout,
		LockTimeout:      cfg.ProjectDBLockTimeout,
	}
	return handler.NewTableImportHandler(schemaHandler.NewImportTarget(loader),
		importLimits(cfg, service.NewOrgPlanTiers(store, sqlStore)), tableimport.NewSheetsFetcher(), sqlStore)
}

// importLimits follows the org's plan in cloud mode; a self-hosted platform
// enforces no plan, so it gets the largest allowance.
func importLimits(cfg config.AppConfig, plans service.PlanTiers) handler.ImportLimits {
	return func(ctx context.Context, projectID string) (tableimport.Limits, error) {
		if !cfg.IsCloud() {
			return tableimport.ForTier(domain.Enterprise)
		}
		tier, err := plans.ProjectPlanTier(ctx, projectID)
		if err != nil {
			return tableimport.Limits{}, err
		}
		return tableimport.ForTier(tier)
	}
}
