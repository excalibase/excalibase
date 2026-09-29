package permissions

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/schema"
)

// LegacyData is one project's table grants, row policies and column policies.
type LegacyData struct {
	Grants         []domain.TableGrant
	Policies       []domain.Policy
	ColumnPolicies []domain.ColumnPolicy
}

// LiveSchema is what the project database holds: every table-like relation
// with its columns in ordinal order, and the functions the grants name.
type LiveSchema struct {
	Tables    map[string][]string
	Functions map[string][]schema.FunctionDetail
}

// FoldResult is the permission set a project's legacy data folds into, and
// a line for everything that could not be carried over.
type FoldResult struct {
	Import  domain.LegacyPermissionImport
	Dropped []string
}

const legacyAuthenticatedRole = "authenticated"

var permissionOperations = []string{
	domain.PermissionSelect, domain.PermissionInsert, domain.PermissionUpdate, domain.PermissionDelete,
}

var legacyOperations = map[domain.Operation]string{
	domain.OpSelect: domain.PermissionSelect, domain.OpInsert: domain.PermissionInsert,
	domain.OpUpdate: domain.PermissionUpdate, domain.OpDelete: domain.PermissionDelete,
}

type permissionKey struct{ table, role, op string }

type tableOp struct{ table, op string }

// foldedPolicy is a row policy converted once and applied to every table and
// operation it names.
type foldedPolicy struct {
	name   string
	effect domain.PolicyEffect
	exp    Exp
	err    error
	roles  map[string]bool
	all    bool
}

func (p *foldedPolicy) appliesTo(role string) bool {
	return p.all || p.roles[role]
}

type folder struct {
	live         LiveSchema
	dropped      []string
	granted      map[permissionKey]bool
	rolesOnTable map[string]map[string]bool
	allOnTable   map[string]bool
	policies     map[tableOp][]*foldedPolicy
	allowAny     map[tableOp]bool
	functions    map[string]map[string]bool
}

// Fold applies docs/features/permissions.md §9 to one project's legacy data.
// Whatever cannot be expressed fails closed: the permission it would have
// shaped is not written, and the reason is listed in Dropped.
func Fold(legacy LegacyData, live LiveSchema) FoldResult {
	f := &folder{
		live: live, granted: map[permissionKey]bool{}, rolesOnTable: map[string]map[string]bool{},
		allOnTable: map[string]bool{}, policies: map[tableOp][]*foldedPolicy{},
		allowAny: map[tableOp]bool{}, functions: map[string]map[string]bool{},
	}
	for _, g := range legacy.Grants {
		f.addGrant(g)
	}
	for _, p := range legacy.Policies {
		f.addPolicy(p)
	}
	drafts := f.permissions()
	f.applyColumnPolicies(drafts, legacy.ColumnPolicies)
	result := FoldResult{Import: domain.LegacyPermissionImport{Permissions: f.encode(drafts)}}
	result.Import.Functions, result.Import.FunctionPermissions = f.trackedFunctions()
	result.Dropped = f.dropped
	return result
}

func (f *folder) drop(format string, args ...any) {
	f.dropped = append(f.dropped, fmt.Sprintf(format, args...))
}

// legacyRole maps a legacy role name onto the role the engine runs as;
// ok=false skips it.
func (f *folder) legacyRole(source, role string) (string, bool) {
	switch role {
	case legacyAuthenticatedRole:
		return domain.GrantRoleUser, true
	case domain.GrantRoleService:
		return "", false // service bypasses permissions; nothing to carry
	}
	if err := ValidateRole(role); err != nil {
		f.drop("%s: role %q is not a valid role name", source, role)
		return "", false
	}
	return role, true
}

func (f *folder) addGrant(g domain.TableGrant) {
	if !g.Enabled {
		return
	}
	source := fmt.Sprintf("table grant %s on %s", g.ID, g.Resource)
	role, ok := f.legacyRole(source, g.Role)
	if !ok {
		return
	}
	tables := f.resolveTables(source, g.Resource)
	functions := resolveKeys(g.Resource, f.live.Functions)
	if len(tables) == 0 && len(functions) == 0 {
		f.drop("%s: no such table or function in the project database", source)
		return
	}
	for _, table := range tables {
		f.roleOnTable(table, role)
		for _, op := range g.Operations {
			if name, known := legacyOperations[op]; known {
				f.granted[permissionKey{table, role, name}] = true
			}
		}
	}
	for _, fn := range functions {
		f.addFunctionGrant(source, fn, role, g.Operations)
	}
}

func (f *folder) addFunctionGrant(source, fn, role string, ops []domain.Operation) {
	for _, op := range ops {
		if op == domain.OpSelect {
			if f.functions[fn] == nil {
				f.functions[fn] = map[string]bool{}
			}
			f.functions[fn][role] = true
			return
		}
	}
	f.drop("%s: a function is only callable through a SELECT grant", source)
}

func (f *folder) roleOnTable(table, role string) {
	if f.rolesOnTable[table] == nil {
		f.rolesOnTable[table] = map[string]bool{}
	}
	f.rolesOnTable[table][role] = true
}

func (f *folder) addPolicy(p domain.Policy) {
	if !p.Enabled {
		return
	}
	source := fmt.Sprintf("row policy %s (%s) on %s", p.ID, p.Name, p.Resource)
	folded := &foldedPolicy{name: p.Name, effect: p.Effect, roles: map[string]bool{}}
	folded.exp, folded.err = ConvertRules(p.RuleLogic, p.Rules)
	if folded.err != nil {
		f.drop("%s: %v", source, folded.err)
	}
	f.assign(source, folded, p.Assignments)
	for _, table := range f.resolveTables(source, p.Resource) {
		f.addPolicyToTable(table, folded, p.Operations)
	}
}

func (f *folder) assign(source string, folded *foldedPolicy, assignments []domain.Assignment) {
	for _, a := range assignments {
		switch a.TargetType {
		case domain.TargetAll:
			folded.all = true
		case domain.TargetRole:
			if role, ok := f.legacyRole(source, a.TargetID); ok {
				folded.roles[role] = true
			}
		default:
			f.drop("%s: %s assignment %q is not carried over", source, a.TargetType, a.TargetID)
		}
	}
}

func (f *folder) addPolicyToTable(table string, folded *foldedPolicy, ops []domain.Operation) {
	for role := range folded.roles {
		f.roleOnTable(table, role)
	}
	if folded.all {
		f.allOnTable[table] = true
	}
	if len(ops) == 0 {
		ops = []domain.Operation{domain.OpSelect, domain.OpInsert, domain.OpUpdate, domain.OpDelete}
	}
	for _, op := range ops {
		name, known := legacyOperations[op]
		if !known {
			continue
		}
		key := tableOp{table, name}
		f.policies[key] = append(f.policies[key], folded)
		if folded.effect == domain.EffectAllow {
			f.allowAny[key] = true
		}
	}
}

// resolveTables finds the served tables a legacy resource names. A bare name
// matches that table in every served schema, as the engine matched it.
func (f *folder) resolveTables(source, resource string) []string {
	var served []string
	for _, key := range resolveKeys(resource, f.live.Tables) {
		schemaName, _, _ := strings.Cut(key, ".")
		if !IsServedSchema(schemaName) {
			continue
		}
		if err := ValidateQualifiedName(key); err != nil {
			f.drop("%s: table %s cannot be named by a permission (not lower-case identifiers)", source, key)
			continue
		}
		served = append(served, key)
	}
	return served
}

func resolveKeys[V any](resource string, objects map[string]V) []string {
	var keys []string
	if strings.Contains(resource, ".") {
		if _, ok := objects[resource]; ok {
			keys = append(keys, resource)
		}
		return keys
	}
	for key := range objects {
		if _, name, _ := strings.Cut(key, "."); name == resource {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// draft is a permission before encoding; columns may still narrow.
type draft struct {
	key  permissionKey
	body map[string]any
}

func (f *folder) permissions() []*draft {
	tables := map[string]bool{}
	for table := range f.rolesOnTable {
		tables[table] = true
	}
	for table := range f.allOnTable {
		tables[table] = true
	}
	var drafts []*draft
	for _, table := range sortedKeys(tables) {
		for _, role := range sortedKeys(f.rolesFor(table)) {
			for _, op := range permissionOperations {
				if body, ok := f.permission(permissionKey{table, role, op}); ok {
					drafts = append(drafts, &draft{key: permissionKey{table, role, op}, body: body})
				}
			}
		}
	}
	return drafts
}

// rolesFor is every role a table's grants or policies name, plus anon and
// user when a policy is assigned to ALL.
func (f *folder) rolesFor(table string) map[string]bool {
	roles := map[string]bool{}
	for role := range f.rolesOnTable[table] {
		roles[role] = true
	}
	if f.allOnTable[table] {
		roles[domain.GrantRoleAnon] = true
		roles[domain.GrantRoleUser] = true
	}
	return roles
}

// permission reproduces the old engine's rule for one table, role and
// operation: a matching DENY hides the row; once any ALLOW exists for the
// table and operation, a row needs a matching ALLOW in scope for the caller.
func (f *folder) permission(key permissionKey) (map[string]any, bool) {
	scope := f.inScope(key)
	if !scope.reachable {
		return nil, false
	}
	if scope.blockedBy != "" {
		f.dropPermission(key, fmt.Sprintf("row policy %q denies every row or could not be carried over", scope.blockedBy))
		return nil, false
	}
	parts := scope.denies
	if f.allowAny[tableOp{key.table, key.op}] {
		if len(scope.allows) == 0 {
			f.dropPermission(key, "no row policy lets this role reach a row")
			return nil, false
		}
		parts = append([]Exp{Or(scope.allows...)}, parts...)
	}
	exp := And(parts...)
	if err := validateExp(exp); err != nil {
		f.dropPermission(key, err.Error())
		return nil, false
	}
	return definitionFor(key.op, exp), true
}

type policyScope struct {
	reachable bool
	allows    []Exp
	denies    []Exp
	blockedBy string
}

// inScope collects the policies that apply to one table, role and operation.
// The role reaches the operation through a grant or an ALLOW in scope; a
// DENY never grants.
func (f *folder) inScope(key permissionKey) policyScope {
	scope := policyScope{reachable: f.granted[key]}
	for _, p := range f.policies[tableOp{key.table, key.op}] {
		if !p.appliesTo(key.role) {
			continue
		}
		switch {
		case p.effect == domain.EffectAllow:
			scope.reachable = true
			if p.err == nil {
				scope.allows = append(scope.allows, p.exp)
			}
		case p.err != nil || len(p.exp) == 0:
			scope.blockedBy = p.name
		default:
			scope.denies = append(scope.denies, Not(p.exp))
		}
	}
	return scope
}

func (f *folder) dropPermission(key permissionKey, reason string) {
	f.drop("%s|%s|%s: not written: %s", key.table, key.role, key.op, reason)
}

func validateExp(exp Exp) error {
	raw, err := json.Marshal(exp)
	if err != nil {
		return err
	}
	return ValidateBoolExp(raw)
}

func definitionFor(op string, exp Exp) map[string]any {
	switch op {
	case domain.PermissionSelect:
		return map[string]any{keyFilter: exp, keyColumns: allColumns, keyAllowAggregations: false}
	case domain.PermissionInsert:
		return map[string]any{keyCheck: exp, keyColumns: allColumns}
	case domain.PermissionUpdate:
		// As in Postgres, a policy's USING doubles as its WITH CHECK, so a
		// row cannot be updated out of the rows the role may reach.
		return map[string]any{keyFilter: exp, keyCheck: exp, keyColumns: allColumns}
	}
	return map[string]any{keyFilter: exp}
}

func (f *folder) encode(drafts []*draft) []domain.TablePermission {
	out := make([]domain.TablePermission, 0, len(drafts))
	for _, d := range drafts {
		raw, err := json.Marshal(d.body)
		if err != nil {
			f.drop("%s|%s|%s: not written: %v", d.key.table, d.key.role, d.key.op, err)
			continue
		}
		out = append(out, domain.TablePermission{
			Table: d.key.table, Role: d.key.role, Operation: d.key.op, Definition: raw,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Table != b.Table {
			return a.Table < b.Table
		}
		if a.Role != b.Role {
			return a.Role < b.Role
		}
		return a.Operation < b.Operation
	})
	return out
}

func (f *folder) trackedFunctions() ([]domain.TrackedFunction, []domain.FunctionPermission) {
	functions := []domain.TrackedFunction{}
	grants := []domain.FunctionPermission{}
	for _, fn := range sortedKeys(f.functions) {
		decision, err := Trackable(f.live.Functions[fn], nil)
		if err != nil {
			f.drop("function %s: cannot be tracked: %v", fn, err)
			continue
		}
		// Inference off: only the granted roles could call it before.
		functions = append(functions, domain.TrackedFunction{Function: fn, ExposedAs: decision.ExposedAs})
		for _, role := range sortedKeys(f.functions[fn]) {
			grants = append(grants, domain.FunctionPermission{Function: fn, Role: role})
		}
	}
	return functions, grants
}

func sortedKeys[V any](set map[string]V) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
