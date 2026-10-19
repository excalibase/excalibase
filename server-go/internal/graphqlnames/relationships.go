package graphqlnames

import (
	"sort"
	"strings"
	"unicode"

	"github.com/excalibase/provisioning-poc/internal/schema"
)

// Relationship is one nested field the engine adds to a table for a
// foreign key: an object field on the table holding the key, an array field
// on the table it points at. Field is the name to use in queries and
// permission filters; DeprecatedName is the older name that still answers.
type Relationship struct {
	Field          string   `json:"field"`
	Kind           string   `json:"kind"`
	Table          string   `json:"table"`
	Columns        []string `json:"columns"`
	DeprecatedName string   `json:"deprecatedName,omitempty"`
}

// foreignKey is one constraint, its column rows folded together.
type foreignKey struct {
	child, target string
	columns       []string
}

// relationshipNaming holds one table's relationship names while aliases are chosen.
type relationshipNaming struct {
	fields  map[string]Relationship // canonical name → field
	columns map[string]bool
}

// Relationships names every relationship among one schema's tables as the
// engine does (excalibase-graphql SchemaInfo.addForeignKey/addReverseFk and
// RelationshipAliases). tables maps each table to its column names; a key
// row whose other end is not among them is left out.
func Relationships(schemaName string, tables map[string][]string, rows []schema.RelationshipInfo) map[string][]Relationship {
	naming := make(map[string]*relationshipNaming, len(tables))
	for table, names := range tables {
		columns := make(map[string]bool, len(names))
		for _, column := range names {
			columns[column] = true
		}
		naming[table] = &relationshipNaming{fields: map[string]Relationship{}, columns: columns}
	}
	keys := foldForeignKeys(rows, naming)
	prefix := LowerCamel(schemaName)
	for _, key := range keys {
		name := prefix + Pascal(key.target)
		if len(key.columns) == 1 {
			name = prefix + Pascal(key.columns[0])
		}
		naming[key.child].fields[name] = Relationship{Field: name, Kind: "object", Table: key.target, Columns: key.columns}
	}
	siblings := map[string][]foreignKey{}
	for _, key := range keys {
		siblings[key.target+"|"+key.child] = append(siblings[key.target+"|"+key.child], key)
	}
	for _, group := range siblings {
		for _, key := range group {
			name := prefix + Pascal(key.child)
			if len(group) > 1 {
				for _, column := range key.columns {
					name += Pascal(LowerCamel(column))
				}
			}
			naming[key.target].fields[name] = Relationship{Field: name, Kind: "array", Table: key.child, Columns: key.columns}
		}
	}
	return applyAliases(prefix, naming, siblings)
}

// foldForeignKeys groups the per-column rows by constraint, in row order.
func foldForeignKeys(rows []schema.RelationshipInfo, naming map[string]*relationshipNaming) []foreignKey {
	var keys []foreignKey
	index := map[string]int{}
	for _, row := range rows {
		if naming[row.SourceTable] == nil || naming[row.TargetTable] == nil {
			continue
		}
		id := row.SourceTable + "|" + row.ConstraintName
		if at, seen := index[id]; seen {
			keys[at].columns = append(keys[at].columns, row.SourceColumn)
			continue
		}
		index[id] = len(keys)
		keys = append(keys, foreignKey{child: row.SourceTable, target: row.TargetTable, columns: []string{row.SourceColumn}})
	}
	return keys
}

// applyAliases gives a relationship its readable name where the engine does,
// and returns each table's fields sorted by name.
func applyAliases(prefix string, naming map[string]*relationshipNaming, siblings map[string][]foreignKey) map[string][]Relationship {
	out := make(map[string][]Relationship, len(naming))
	for table, names := range naming {
		proposed, clashing := map[string]string{}, map[string]bool{}
		for canonical, field := range names.fields {
			alias := relationshipAlias(prefix, field, len(siblings[table+"|"+field.Table]) > 1)
			if alias == "" {
				continue
			}
			if _, taken := proposed[alias]; taken || alias == canonical {
				clashing[alias] = true
			}
			proposed[alias] = canonical
		}
		fields := make([]Relationship, 0, len(names.fields))
		renamed := map[string]string{}
		for alias, canonical := range proposed {
			if !clashing[alias] && !names.columns[alias] && names.fields[alias].Field == "" {
				renamed[canonical] = alias
			}
		}
		for canonical, field := range names.fields {
			if alias, ok := renamed[canonical]; ok {
				field.Field, field.DeprecatedName = alias, canonical
			}
			fields = append(fields, field)
		}
		sort.Slice(fields, func(i, j int) bool { return fields[i].Field < fields[j].Field })
		if len(fields) > 0 {
			out[table] = fields
		}
	}
	return out
}

// relationshipAlias is RelationshipAliases' proposal for one field, or "".
// An object field drops its key column's id; one of several array fields
// from the same child table is named By its key.
func relationshipAlias(prefix string, field Relationship, sharedArray bool) string {
	if len(field.Columns) != 1 {
		return ""
	}
	column := field.Columns[0]
	if field.Kind == "object" {
		if stripped := withoutID(column); stripped != "" {
			return prefix + Pascal(stripped)
		}
		return ""
	}
	if !sharedArray {
		return ""
	}
	key := withoutID(column)
	if key == "" {
		key = column
	}
	return prefix + Pascal(field.Table) + "By" + Pascal(LowerCamel(key))
}

// withoutID is RelationshipAliases.withoutId: home_team_id → home_team,
// homeTeamId → homeTeam, "" when the column has no id suffix.
func withoutID(column string) string {
	if len(column) > 3 && strings.HasSuffix(strings.ToLower(column), "_id") {
		return column[:len(column)-3]
	}
	if len(column) > 2 && strings.HasSuffix(column, "Id") && unicode.IsLower(rune(column[len(column)-3])) {
		return column[:len(column)-2]
	}
	return ""
}
