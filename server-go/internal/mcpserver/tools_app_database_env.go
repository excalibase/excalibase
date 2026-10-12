package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

var (
	postgresVariables = []string{"DATABASE_URL", "PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD"}
	mysqlVariables    = []string{"DATABASE_URL", "MYSQL_HOST", "MYSQL_PORT", "MYSQL_DATABASE", "MYSQL_USER", "MYSQL_PASSWORD"}
)

type referenceTarget struct {
	SourceKind string `json:"sourceKind"`
	SourceName string `json:"sourceName"`
	Variable   string `json:"variable"`
}

type referenceVariable struct {
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	Reference referenceTarget `json:"reference"`
}

type projectDatabase struct {
	ProjectID    string `json:"projectId"`
	NoDatabase   bool   `json:"noDatabase"`
	DatabaseName string `json:"databaseName"`
	DatabaseType string `json:"databaseType"`
}

// databaseReferences declares each variable as a reference to the project's
// database, the same entry Studio's variable picker stores; the deploy
// resolves it, so the credential never passes through the conversation.
func databaseReferences(ctx context.Context, c *call, projectID string, in updateAppArgs) ([]json.RawMessage, error) {
	for _, literal := range in.SetEnv {
		if slices.Contains(in.DatabaseEnv, literal.Name) {
			return nil, fmt.Errorf("%s is named in both set_env and database_env; pick one", literal.Name)
		}
	}
	database, err := findProjectDatabase(ctx, c, projectID)
	if err != nil {
		return nil, err
	}
	exposed := postgresVariables
	if strings.EqualFold(database.DatabaseType, "mysql") {
		exposed = mysqlVariables
	}
	references := make([]json.RawMessage, 0, len(in.DatabaseEnv))
	for _, variable := range in.DatabaseEnv {
		if !slices.Contains(exposed, variable) {
			return nil, fmt.Errorf("the project's database exposes no variable %q; it exposes %s", variable, strings.Join(exposed, ", "))
		}
		reference, err := json.Marshal(referenceVariable{Name: variable, Kind: "reference",
			Reference: referenceTarget{SourceKind: "database", SourceName: database.DatabaseName, Variable: variable}})
		if err != nil {
			return nil, err
		}
		references = append(references, reference)
	}
	return references, nil
}

func findProjectDatabase(ctx context.Context, c *call, projectID string) (projectDatabase, error) {
	var projects []projectDatabase
	if err := c.get(ctx, provisionAPI, nil, &projects); err != nil {
		return projectDatabase{}, err
	}
	for _, project := range projects {
		if project.ProjectID != projectID {
			continue
		}
		if project.NoDatabase || project.DatabaseName == "" {
			return projectDatabase{}, fmt.Errorf("project %s has no database to read variables from", projectID)
		}
		return project, nil
	}
	return projectDatabase{}, fmt.Errorf("project %s was not found", projectID)
}
