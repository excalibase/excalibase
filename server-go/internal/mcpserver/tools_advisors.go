package mcpserver

import (
	"context"

	"github.com/excalibase/provisioning-poc/internal/features"
)

const advisorsNote = "fixSql is a suggestion and is never applied by the platform: show it to the developer and run it only once they agree " +
	"(execute_sql, or apply_migration for one that is not CONCURRENTLY). A check with status unavailable was not read, so it reports nothing either way."

func advisorTools() []entry {
	return []entry{
		dark(features.Advisors, tool("get_advisors", "Security and performance checks of the project's database and API permissions, Postgres or MySQL: "+
			"foreign keys without an index, unused and duplicate indexes, tables without a primary key, large tables read by full scans, "+
			"tables anon reaches with no row filter, and writes a role cannot read back. Each finding has a level, a fix hint and, where safe, fixSql. "+
			"Read-only: nothing is changed.", readTool, getAdvisors)),
	}
}

func getAdvisors(ctx context.Context, c *call, in projectArg) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	var report map[string]any
	if err := c.get(ctx, provisionAPI+projectID+"/advisors", nil, &report); err != nil {
		return nil, err
	}
	report["note"] = advisorsNote
	return report, nil
}
