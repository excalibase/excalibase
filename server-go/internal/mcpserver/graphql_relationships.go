package mcpserver

import (
	"github.com/excalibase/provisioning-poc/internal/graphqlnames"
	"github.com/excalibase/provisioning-poc/internal/schema"
)

// relationshipField is one nested field the engine adds for a foreign key.
type relationshipField = graphqlnames.Relationship

// relationshipFields names every relationship among tables as the engine does.
func relationshipFields(schemaName string, tables []tableColumns, rows []schema.RelationshipInfo) map[string][]relationshipField {
	columns := make(map[string][]string, len(tables))
	for _, table := range tables {
		names := make([]string, 0, len(table.Columns))
		for _, column := range table.Columns {
			names = append(names, column.Name)
		}
		columns[table.Name] = names
	}
	return graphqlnames.Relationships(schemaName, columns, rows)
}
