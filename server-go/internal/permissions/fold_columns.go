package permissions

import (
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// hiddenColumns collects, per table, the columns column policies hide from
// one role and from every role.
type hiddenColumns struct {
	byRole map[permissionKey]map[string]bool
	all    map[string]map[string]bool
}

// applyColumnPolicies narrows select columns: a HIDE or NULL mask for a role
// leaves the column out of that role's select list (§9). Only masks on reads
// ever applied, so masks naming other operations only are skipped.
func (f *folder) applyColumnPolicies(drafts []*draft, policies []domain.ColumnPolicy) {
	hidden := hiddenColumns{byRole: map[permissionKey]map[string]bool{}, all: map[string]map[string]bool{}}
	for _, p := range policies {
		f.addColumnPolicy(&hidden, p)
	}
	for _, d := range drafts {
		if d.key.op != domain.PermissionSelect {
			continue
		}
		mask := union(hidden.all[d.key.table], hidden.byRole[permissionKey{d.key.table, d.key.role, ""}])
		if f.masksLiveColumn(d.key.table, mask) {
			d.body[keyColumns] = f.visibleColumns(d.key, mask)
		}
	}
}

func (f *folder) addColumnPolicy(hidden *hiddenColumns, p domain.ColumnPolicy) {
	if !p.Enabled || !appliesToReads(p.Operations) {
		return
	}
	source := fmt.Sprintf("column policy %s (%s) on %s", p.ID, p.Name, p.Resource)
	if p.Mode != domain.MaskHide && p.Mode != domain.MaskNull {
		f.drop("%s: %s masks are not carried over; the columns stay visible", source, p.Mode)
		return
	}
	for _, table := range f.resolveTables(source, p.Resource) {
		for _, a := range p.Assignments {
			f.hideFor(hidden, source, table, a, p.Columns)
		}
	}
}

func (f *folder) hideFor(hidden *hiddenColumns, source, table string, a domain.Assignment, columns []string) {
	var target map[string]bool
	switch a.TargetType {
	case domain.TargetAll:
		if hidden.all[table] == nil {
			hidden.all[table] = map[string]bool{}
		}
		target = hidden.all[table]
	case domain.TargetRole:
		role, ok := f.legacyRole(source, a.TargetID)
		if !ok {
			return
		}
		key := permissionKey{table, role, ""}
		if hidden.byRole[key] == nil {
			hidden.byRole[key] = map[string]bool{}
		}
		target = hidden.byRole[key]
	default:
		f.drop("%s: %s assignment %q is not carried over", source, a.TargetType, a.TargetID)
		return
	}
	for _, column := range columns {
		target[column] = true
	}
}

func appliesToReads(ops []domain.Operation) bool {
	if len(ops) == 0 {
		return true
	}
	for _, op := range ops {
		if op == domain.OpSelect {
			return true
		}
	}
	return false
}

// masksLiveColumn reports whether a mask hides a column the table has; one
// that hides none leaves the role on "*".
func (f *folder) masksLiveColumn(table string, mask map[string]bool) bool {
	for _, column := range f.live.Tables[table] {
		if mask[column] {
			return true
		}
	}
	return false
}

// visibleColumns is the table's live columns minus the masked ones, in
// ordinal order. A column no permission can name is left out, never widened.
func (f *folder) visibleColumns(key permissionKey, mask map[string]bool) []string {
	visible := []string{}
	for _, column := range f.live.Tables[key.table] {
		if mask[column] {
			continue
		}
		if !columnName.MatchString(column) {
			f.drop("%s|%s|select: column %q cannot be named by a permission and is left out", key.table, key.role, column)
			continue
		}
		visible = append(visible, column)
	}
	return visible
}

func union(sets ...map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, set := range sets {
		for key := range set {
			out[key] = true
		}
	}
	return out
}
