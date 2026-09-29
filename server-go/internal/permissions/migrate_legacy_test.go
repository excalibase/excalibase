package permissions

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/schema"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

type fakeMigrationStore struct {
	storage.PermissionStore
	pending    []string
	pendingErr error
	imported   map[string]domain.LegacyPermissionImport
}

func (f *fakeMigrationStore) LegacyPending(context.Context) ([]string, error) {
	return f.pending, f.pendingErr
}

func (f *fakeMigrationStore) ImportLegacy(_ context.Context, projectID string, set domain.LegacyPermissionImport) error {
	f.imported[projectID] = set
	return nil
}

type fakeLegacyGrants struct {
	storage.TableGrantStore
	grants map[string][]domain.TableGrant
}

func (f fakeLegacyGrants) ListGrants(_ context.Context, projectID string) ([]domain.TableGrant, error) {
	return f.grants[projectID], nil
}

type fakeLegacyPolicies struct {
	storage.RlsPolicyStore
	policies map[string][]domain.Policy
	columns  map[string][]domain.ColumnPolicy
}

func (f fakeLegacyPolicies) ListRls(_ context.Context, projectID, _ string) ([]domain.Policy, error) {
	return f.policies[projectID], nil
}

func (f fakeLegacyPolicies) ListColumn(_ context.Context, projectID, _ string) ([]domain.ColumnPolicy, error) {
	return f.columns[projectID], nil
}

type fakeLive struct {
	tables    map[string]map[string][]string
	functions []schema.FunctionDetail
	asked     []string
}

func (f *fakeLive) TableColumns(_ context.Context, projectID string) (map[string][]string, error) {
	tables, ok := f.tables[projectID]
	if !ok {
		return nil, errors.New("connection refused")
	}
	return tables, nil
}

func (f *fakeLive) FunctionDetails(_ context.Context, _, schemaName, name string) ([]schema.FunctionDetail, error) {
	f.asked = append(f.asked, schemaName+"|"+name)
	var out []schema.FunctionDetail
	for _, fn := range f.functions {
		if fn.Name == name && (schemaName == "" || fn.Schema == schemaName) {
			out = append(out, fn)
		}
	}
	return out, nil
}

type countingPublisher struct{ events []domain.PolicyChangeEvent }

func (c *countingPublisher) PublishPolicyChange(_ context.Context, evt domain.PolicyChangeEvent) {
	c.events = append(c.events, evt)
}

func TestLegacyMigrator_MigratesReachableProjectsAndRetriesTheRest(t *testing.T) {
	store := &fakeMigrationStore{pending: []string{"p1", "p2"}, imported: map[string]domain.LegacyPermissionImport{}}
	grants := fakeLegacyGrants{grants: map[string][]domain.TableGrant{
		"p1": {grant("orders", "anon", domain.OpSelect), grant("search_orders", "editor", domain.OpSelect)},
		"p2": {grant("orders", "anon", domain.OpSelect)},
	}}
	policies := fakeLegacyPolicies{policies: map[string][]domain.Policy{
		"p1": {policy("p", domain.EffectAllow, "orders", selectOp, toRole("user"), ownerRule)},
	}}
	live := &fakeLive{
		tables:    map[string]map[string][]string{"p1": {ordersKey: {"id", "owner_id"}}},
		functions: searchOrders(nil),
	}
	published := &countingPublisher{}

	migrated, err := NewLegacyMigrator(store, grants, policies, live, published).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if migrated != 1 {
		t.Fatalf("migrated %d projects, want 1 (p2's database is unreachable)", migrated)
	}
	if _, retried := store.imported["p2"]; retried {
		t.Fatal("an unreachable project must be left for the next start, not marked")
	}
	set := store.imported["p1"]
	if len(set.Permissions) != 1 || len(set.Functions) != 1 || len(set.FunctionPermissions) != 1 {
		t.Fatalf("p1 import = %+v", set)
	}
	if len(published.events) != 1 || published.events[0].ProjectID != "p1" {
		t.Fatalf("events = %+v", published.events)
	}
	if len(live.asked) != 2 || live.asked[0] != "|orders" || live.asked[1] != "|search_orders" {
		t.Fatalf("function lookups = %v", live.asked)
	}
}

func TestLegacyMigrator_PendingListFailureIsAnError(t *testing.T) {
	store := &fakeMigrationStore{pendingErr: errors.New("down")}
	if _, err := NewLegacyMigrator(store, fakeLegacyGrants{}, fakeLegacyPolicies{}, &fakeLive{}, nil).Run(context.Background()); err == nil {
		t.Fatal("want an error")
	}
}
