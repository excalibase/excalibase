package service

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// renderedStatementTimeout builds a CNPG cluster from a resolved tier and
// reads back the parameter admission actually sets, so the assertion
// exercises the same path a tenant's cluster is provisioned with.
func renderedStatementTimeout(t *testing.T, tc config.TierConfig) (interface{}, bool) {
	t.Helper()
	obj := k8s.BuildPostgreSQLCluster(k8s.PostgreSQLClusterOpts{
		ProjectID: "proj-stmt", Namespace: "ns-stmt", Tier: tc,
	})
	spec := obj.Object["spec"].(map[string]interface{})
	params := spec["postgresql"].(map[string]interface{})["parameters"].(map[string]interface{})
	v, ok := params["statement_timeout"]
	return v, ok
}

// A table-backed row with no statement timeout (the tier_configs table has
// no such column) must still end up with the built-in tier's value on the
// rendered cluster — a tenant never gets an unbounded query time limit just
// because the tier resolved via the DB store.
func TestTierConfig_TableBackedRowFallsBackToBuiltinStatementTimeout(t *testing.T) {
	row := config.TierConfig{MaxProjects: 5, Instances: 1, StorageSize: "50Gi", Memory: "4Gi", CPU: "2"}
	svc := NewProvisioningService(nil, nil, nil)
	svc.SetTierStore(fakeTierStore{m: map[domain.TierType]config.TierConfig{domain.Standard: row}})

	got, err := svc.tierConfig(context.Background(), domain.Standard)
	if err != nil {
		t.Fatalf("tierConfig: %v", err)
	}
	builtin, _ := config.GetTierConfig(domain.Standard)
	if got.StatementTimeout != builtin.StatementTimeout {
		t.Fatalf("StatementTimeout = %q, want built-in %q", got.StatementTimeout, builtin.StatementTimeout)
	}

	v, ok := renderedStatementTimeout(t, got)
	if !ok || v != builtin.StatementTimeout {
		t.Errorf("rendered statement_timeout = %v (present=%v), want %q", v, ok, builtin.StatementTimeout)
	}
}

// A table-backed row that does carry its own statement timeout must keep it
// rather than being overridden by the built-in value.
func TestTierConfig_TableBackedRowKeepsOwnStatementTimeout(t *testing.T) {
	row := config.TierConfig{MaxProjects: 5, Instances: 1, StorageSize: "50Gi", Memory: "4Gi", CPU: "2", StatementTimeout: "45s"}
	svc := NewProvisioningService(nil, nil, nil)
	svc.SetTierStore(fakeTierStore{m: map[domain.TierType]config.TierConfig{domain.Standard: row}})

	got, err := svc.tierConfig(context.Background(), domain.Standard)
	if err != nil {
		t.Fatalf("tierConfig: %v", err)
	}
	if got.StatementTimeout != "45s" {
		t.Fatalf("StatementTimeout = %q, want %q", got.StatementTimeout, "45s")
	}

	v, ok := renderedStatementTimeout(t, got)
	if !ok || v != "45s" {
		t.Errorf("rendered statement_timeout = %v (present=%v), want 45s", v, ok)
	}
}

// A tier name the built-ins don't know, with no timeout on its row either,
// must fail provisioning rather than silently proceed with no query limit.
func TestTierConfig_UnknownTierWithoutStatementTimeoutFails(t *testing.T) {
	row := config.TierConfig{MaxProjects: 1, Instances: 1, StorageSize: "5Gi", Memory: "1Gi", CPU: "1"}
	svc := NewProvisioningService(nil, nil, nil)
	svc.SetTierStore(fakeTierStore{m: map[domain.TierType]config.TierConfig{domain.TierType("PLATINUM"): row}})

	if _, err := svc.tierConfig(context.Background(), domain.TierType("PLATINUM")); err == nil {
		t.Fatal("expected an error for an unknown tier with no statement timeout")
	}
}
