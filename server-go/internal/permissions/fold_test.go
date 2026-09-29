package permissions

import (
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/schema"
)

const (
	ordersKey      = "public.orders"
	userOwnsRow    = `{"owner_id":{"_eq":"X-Excalibase-User-Id"}}`
	openRows       = `{"status":{"_eq":"open"}}`
	selectAll      = `{"allowAggregations":false,"columns":"*","filter":{}}`
	selectOwnedAll = `{"allowAggregations":false,"columns":"*","filter":` + userOwnsRow + `}`
)

func liveOrders() LiveSchema {
	return LiveSchema{Tables: map[string][]string{
		"public.orders": {"id", "owner_id", "email", "status"},
		"sales.orders":  {"id"},
		"public.users":  {"id", "email"},
		"auth.users":    {"id", "password_hash"},
		"public.Mixed":  {"id"},
	}}
}

func grant(resource, role string, ops ...domain.Operation) domain.TableGrant {
	return domain.TableGrant{ID: resource + role, Resource: resource, Role: role, Operations: ops, Enabled: true}
}

func policy(id string, effect domain.PolicyEffect, resource string, ops []domain.Operation,
	assignments []domain.Assignment, rules ...domain.Rule) domain.Policy {
	return domain.Policy{ID: id, Name: id, Resource: resource, Effect: effect, Operations: ops,
		RuleLogic: domain.LogicAnd, Enabled: true, Rules: rules, Assignments: assignments}
}

func toRole(name string) []domain.Assignment {
	return []domain.Assignment{{TargetType: domain.TargetRole, TargetID: name}}
}

var toAll = []domain.Assignment{{TargetType: domain.TargetAll}}

var (
	ownerRule = rule("owner_id", domain.FieldUUID, domain.OpEQ, "{{currentUserId}}")
	openRule  = rule("status", domain.FieldString, domain.OpEQ, "open")
	nowRule   = rule("created", domain.FieldDatetime, domain.OpLT, "{{now}}")
	selectOp  = []domain.Operation{domain.OpSelect}
)

// permissionMap keys each folded permission as table|role|operation.
func permissionMap(result FoldResult) map[string]string {
	out := map[string]string{}
	for _, p := range result.Import.Permissions {
		out[p.Table+"|"+p.Role+"|"+p.Operation] = string(p.Definition)
	}
	return out
}

func assertPermissions(t *testing.T, result FoldResult, want map[string]string) {
	t.Helper()
	got := permissionMap(result)
	for key, definition := range want {
		if got[key] != definition {
			t.Errorf("%s = %s, want %s", key, got[key], definition)
		}
	}
	for key, definition := range got {
		if _, expected := want[key]; !expected {
			t.Errorf("unexpected permission %s = %s", key, definition)
		}
	}
}

func assertDropped(t *testing.T, result FoldResult, fragments ...string) {
	t.Helper()
	joined := strings.Join(result.Dropped, "\n")
	for _, fragment := range fragments {
		if !strings.Contains(joined, fragment) {
			t.Errorf("dropped list %q lacks %q", result.Dropped, fragment)
		}
	}
}

func TestFold_Grants(t *testing.T) {
	disabled := grant(ordersKey, "editor", domain.OpSelect)
	disabled.Enabled = false
	result := Fold(LegacyData{Grants: []domain.TableGrant{
		grant(ordersKey, "anon", domain.OpSelect),
		grant("orders", "user", domain.OpInsert, domain.OpUpdate, domain.OpDelete),
		grant("users", "user", domain.OpSelect),
		grant("public.gone", "user", domain.OpSelect),
		grant("service_role_table", "service", domain.OpSelect),
		disabled,
	}}, liveOrders())

	assertPermissions(t, result, map[string]string{
		"public.orders|anon|select": selectAll,
		"public.orders|user|insert": `{"check":{},"columns":"*"}`,
		"public.orders|user|update": `{"check":{},"columns":"*","filter":{}}`,
		"public.orders|user|delete": `{"filter":{}}`,
		"sales.orders|user|insert":  `{"check":{},"columns":"*"}`,
		"sales.orders|user|update":  `{"check":{},"columns":"*","filter":{}}`,
		"sales.orders|user|delete":  `{"filter":{}}`,
		// auth.users is never served, so the bare name reaches public.users only.
		"public.users|user|select": selectAll,
	})
	assertDropped(t, result, "public.gone")
}

func TestFold_AuthenticatedRoleBecomesUser(t *testing.T) {
	result := Fold(LegacyData{Policies: []domain.Policy{
		policy("p1", domain.EffectAllow, "orders", selectOp, toRole("authenticated"), ownerRule),
	}}, LiveSchema{Tables: map[string][]string{ordersKey: {"id"}}})
	assertPermissions(t, result, map[string]string{"public.orders|user|select": selectOwnedAll})
}

func TestFold_AllowPolicyWithoutGrantMakesTheTableReachable(t *testing.T) {
	result := Fold(LegacyData{Policies: []domain.Policy{
		policy("p1", domain.EffectAllow, ordersKey, selectOp, toRole("user"), ownerRule),
	}}, liveOrders())
	assertPermissions(t, result, map[string]string{"public.orders|user|select": selectOwnedAll})
}

func TestFold_AllAssignmentReachesAnonUserAndEveryRoleOnTheTable(t *testing.T) {
	result := Fold(LegacyData{
		Grants:   []domain.TableGrant{grant(ordersKey, "editor", domain.OpInsert)},
		Policies: []domain.Policy{policy("p1", domain.EffectAllow, ordersKey, selectOp, toAll, openRule)},
	}, liveOrders())
	selectOpen := `{"allowAggregations":false,"columns":"*","filter":` + openRows + `}`
	assertPermissions(t, result, map[string]string{
		"public.orders|anon|select":   selectOpen,
		"public.orders|user|select":   selectOpen,
		"public.orders|editor|select": selectOpen,
		"public.orders|editor|insert": `{"check":{},"columns":"*"}`,
	})
}

func TestFold_AnAllowForSomeoneElseHidesTheRowsFromAGrantedRole(t *testing.T) {
	// The old engine turned row security on for a table and operation as soon
	// as any ALLOW existed; a role without a matching ALLOW then saw nothing.
	result := Fold(LegacyData{
		Grants:   []domain.TableGrant{grant(ordersKey, "anon", domain.OpSelect)},
		Policies: []domain.Policy{policy("p1", domain.EffectAllow, ordersKey, selectOp, toRole("user"), ownerRule)},
	}, liveOrders())
	assertPermissions(t, result, map[string]string{"public.orders|user|select": selectOwnedAll})
}

func TestFold_AllowsAreOredAndDeniesAndedAsNot(t *testing.T) {
	result := Fold(LegacyData{Policies: []domain.Policy{
		policy("a1", domain.EffectAllow, ordersKey, selectOp, toRole("user"), ownerRule),
		policy("a2", domain.EffectAllow, ordersKey, selectOp, toRole("user"), openRule),
		policy("d1", domain.EffectDeny, ordersKey, selectOp, toRole("user"),
			rule("status", domain.FieldString, domain.OpEQ, "archived")),
	}}, liveOrders())
	assertPermissions(t, result, map[string]string{
		"public.orders|user|select": `{"allowAggregations":false,"columns":"*","filter":{"_and":[{"_or":[` +
			userOwnsRow + `,` + openRows + `]},{"_not":{"status":{"_eq":"archived"}}}]}}`,
	})
}

func TestFold_DenyAloneRestrictsAGrant(t *testing.T) {
	result := Fold(LegacyData{
		Grants:   []domain.TableGrant{grant(ordersKey, "user", domain.OpSelect)},
		Policies: []domain.Policy{policy("d1", domain.EffectDeny, ordersKey, selectOp, toRole("user"), openRule)},
	}, liveOrders())
	assertPermissions(t, result, map[string]string{
		"public.orders|user|select": `{"allowAggregations":false,"columns":"*","filter":{"_not":` + openRows + `}}`,
	})
}

func TestFold_DenyAloneGrantsNothing(t *testing.T) {
	result := Fold(LegacyData{Policies: []domain.Policy{
		policy("d1", domain.EffectDeny, ordersKey, selectOp, toRole("user"), openRule),
	}}, liveOrders())
	assertPermissions(t, result, map[string]string{})
}

func TestFold_DenyEverythingRemovesThePermission(t *testing.T) {
	result := Fold(LegacyData{
		Grants:   []domain.TableGrant{grant(ordersKey, "user", domain.OpSelect)},
		Policies: []domain.Policy{policy("d1", domain.EffectDeny, ordersKey, selectOp, toRole("user"))},
	}, liveOrders())
	assertPermissions(t, result, map[string]string{})
}

func TestFold_UnexpressiblePoliciesFailClosed(t *testing.T) {
	t.Run("allow", func(t *testing.T) {
		result := Fold(LegacyData{
			Grants:   []domain.TableGrant{grant(ordersKey, "user", domain.OpSelect)},
			Policies: []domain.Policy{policy("recent", domain.EffectAllow, ordersKey, selectOp, toRole("user"), nowRule)},
		}, liveOrders())
		assertPermissions(t, result, map[string]string{})
		assertDropped(t, result, "recent", "{{now}}")
	})
	t.Run("deny", func(t *testing.T) {
		result := Fold(LegacyData{
			Grants:   []domain.TableGrant{grant(ordersKey, "user", domain.OpSelect, domain.OpDelete)},
			Policies: []domain.Policy{policy("old", domain.EffectDeny, ordersKey, selectOp, toRole("user"), nowRule)},
		}, liveOrders())
		assertPermissions(t, result, map[string]string{"public.orders|user|delete": `{"filter":{}}`})
		assertDropped(t, result, "old", "public.orders|user|select")
	})
}

func TestFold_UserAndGroupAssignmentsAreNotCarriedOver(t *testing.T) {
	result := Fold(LegacyData{
		Grants: []domain.TableGrant{grant(ordersKey, "user", domain.OpSelect)},
		Policies: []domain.Policy{policy("mine", domain.EffectAllow, ordersKey, selectOp,
			[]domain.Assignment{{TargetType: domain.TargetUser, TargetID: "u-1"}, {TargetType: domain.TargetGroup, TargetID: "g"}}, openRule)},
	}, liveOrders())
	// The ALLOW still turned row security on; nobody but u-1 saw rows before.
	assertPermissions(t, result, map[string]string{})
	assertDropped(t, result, "mine", "USER", "GROUP")
}

func TestFold_OperationsPickFilterOrCheck(t *testing.T) {
	result := Fold(LegacyData{Policies: []domain.Policy{
		policy("p1", domain.EffectAllow, ordersKey, nil, toRole("user"), ownerRule),
	}}, liveOrders())
	assertPermissions(t, result, map[string]string{
		"public.orders|user|select": selectOwnedAll,
		"public.orders|user|insert": `{"check":` + userOwnsRow + `,"columns":"*"}`,
		"public.orders|user|update": `{"check":` + userOwnsRow + `,"columns":"*","filter":` + userOwnsRow + `}`,
		"public.orders|user|delete": `{"filter":` + userOwnsRow + `}`,
	})
}

func TestFold_ServiceAndInvalidRolesAreSkipped(t *testing.T) {
	result := Fold(LegacyData{Policies: []domain.Policy{
		policy("p1", domain.EffectAllow, ordersKey, selectOp, toRole("service"), ownerRule),
		policy("p2", domain.EffectAllow, ordersKey, selectOp, toRole("Bad Role"), ownerRule),
	}}, liveOrders())
	assertPermissions(t, result, map[string]string{})
	assertDropped(t, result, "Bad Role")
}

func TestFold_ColumnPoliciesNarrowSelectColumns(t *testing.T) {
	hide := domain.ColumnPolicy{ID: "c1", Name: "c1", Resource: "orders", Columns: []string{"email"},
		Operations: selectOp, Mode: domain.MaskHide, Enabled: true, Assignments: toRole("user")}
	nullAll := domain.ColumnPolicy{ID: "c2", Name: "c2", Resource: ordersKey, Columns: []string{"status"},
		Mode: domain.MaskNull, Enabled: true, Assignments: toAll}
	partial := domain.ColumnPolicy{ID: "c3", Name: "masked", Resource: ordersKey, Columns: []string{"id"},
		Mode: domain.MaskPartial, Enabled: true, Assignments: toAll}
	insertOnly := domain.ColumnPolicy{ID: "c4", Name: "c4", Resource: ordersKey, Columns: []string{"id"},
		Operations: []domain.Operation{domain.OpInsert}, Mode: domain.MaskHide, Enabled: true, Assignments: toAll}
	result := Fold(LegacyData{
		Grants: []domain.TableGrant{
			grant(ordersKey, "user", domain.OpSelect, domain.OpInsert),
			grant(ordersKey, "anon", domain.OpSelect),
			grant("sales.orders", "user", domain.OpSelect),
		},
		ColumnPolicies: []domain.ColumnPolicy{hide, nullAll, partial, insertOnly},
	}, liveOrders())
	assertPermissions(t, result, map[string]string{
		"public.orders|user|select": `{"allowAggregations":false,"columns":["id","owner_id"],"filter":{}}`,
		"public.orders|user|insert": `{"check":{},"columns":"*"}`,
		"public.orders|anon|select": `{"allowAggregations":false,"columns":["id","owner_id","email"],"filter":{}}`,
		"sales.orders|user|select":  selectAll,
	})
	assertDropped(t, result, "masked", "PARTIAL")
}

func TestFold_FunctionGrantsBecomeTrackedFunctions(t *testing.T) {
	live := liveOrders()
	live.Functions = map[string][]schema.FunctionDetail{
		"public.search_orders": searchOrders(nil),
		"public.bump":          searchOrders(func(d *schema.FunctionDetail) { d.Name = "bump"; d.Volatility = "VOLATILE" }),
		"public.scalar":        searchOrders(func(d *schema.FunctionDetail) { d.Name = "scalar"; d.ReturnsTable = "" }),
	}
	result := Fold(LegacyData{Grants: []domain.TableGrant{
		grant("search_orders", "editor", domain.OpSelect),
		grant("public.search_orders", "user", domain.OpSelect),
		grant("public.bump", "editor", domain.OpSelect),
		grant("public.scalar", "editor", domain.OpSelect),
	}}, live)

	tracked := map[string]domain.TrackedFunction{}
	for _, fn := range result.Import.Functions {
		tracked[fn.Function] = fn
	}
	if len(tracked) != 2 || tracked["public.search_orders"].ExposedAs != domain.ExposeAsQuery ||
		tracked["public.bump"].ExposedAs != domain.ExposeAsMutation {
		t.Fatalf("tracked = %+v", result.Import.Functions)
	}
	for _, fn := range tracked {
		if fn.InferPermissions {
			t.Errorf("%s: inference must be off so only the granted roles may call it", fn.Function)
		}
	}
	want := []domain.FunctionPermission{
		{Function: "public.bump", Role: "editor"},
		{Function: "public.search_orders", Role: "editor"},
		{Function: "public.search_orders", Role: "user"},
	}
	if len(result.Import.FunctionPermissions) != len(want) {
		t.Fatalf("function permissions = %+v", result.Import.FunctionPermissions)
	}
	for i, fp := range want {
		if result.Import.FunctionPermissions[i] != fp {
			t.Errorf("function permission %d = %+v, want %+v", i, result.Import.FunctionPermissions[i], fp)
		}
	}
	assertDropped(t, result, "public.scalar")
}

func TestFold_OutputIsSortedAndValid(t *testing.T) {
	result := Fold(LegacyData{
		Grants: []domain.TableGrant{grant("orders", "user", domain.OpSelect, domain.OpDelete), grant("users", "anon", domain.OpSelect)},
	}, liveOrders())
	previous := ""
	for _, p := range result.Import.Permissions {
		key := p.Table + "|" + p.Role + "|" + p.Operation
		if key <= previous {
			t.Errorf("permissions out of order: %s after %s", key, previous)
		}
		previous = key
		if _, err := NormalizePermission(p.Operation, p.Definition); err != nil {
			t.Errorf("%s fails validation: %v", key, err)
		}
	}
}

func TestFold_MixedCaseTablesAreDropped(t *testing.T) {
	result := Fold(LegacyData{Grants: []domain.TableGrant{grant("Mixed", "user", domain.OpSelect)}}, liveOrders())
	assertPermissions(t, result, map[string]string{})
	assertDropped(t, result, "public.Mixed")
}
