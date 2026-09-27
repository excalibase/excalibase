package handler

import (
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/projectdb"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
)

// schemaHandlerOver builds the handler the way the server does: pools from a
// real projectdb.Opener over v, with the named projects running.
func schemaHandlerOver(t *testing.T, v vaultclient.VaultClient, running ...string) *SchemaHandler {
	t.Helper()
	instances := fakestore.NewInstances()
	for _, id := range running {
		if err := instances.Create(&domain.DatabaseInstance{ProjectID: id, Status: string(domain.StatusActive)}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	pools := projectdb.NewOpener(instances, v, projectdb.OverridesFromEnv(), projectdb.PoolLimits{})
	t.Cleanup(pools.Close)
	return NewSchemaHandler(pools, 30*time.Second)
}
