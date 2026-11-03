// Package advisors runs read-only security and performance checks over a
// project's database catalog and its API permissions (EXC-655). Nothing here
// changes a database: every fix is a suggestion the developer applies.
package advisors

import (
	"sort"
	"strings"
)

// Check ids, stable across engines.
const (
	CheckFKWithoutIndex       = "fk_without_index"
	CheckUnusedIndex          = "unused_index"
	CheckDuplicateIndex       = "duplicate_index"
	CheckNoPrimaryKey         = "table_without_primary_key"
	CheckLargeTableNoIndexUse = "large_table_without_index_use"
	CheckAnonUnfiltered       = "anon_unfiltered_access"
	CheckWriteWithoutSelect   = "write_without_select"
)

const (
	CategorySecurity    = "security"
	CategoryPerformance = "performance"
)

const (
	LevelError   = "error"
	LevelWarning = "warning"
	LevelInfo    = "info"
)

const (
	EnginePostgres = "postgres"
	EngineMySQL    = "mysql"
)

// Whether a check ran on this project.
const (
	StatusRan           = "ran"
	StatusNotApplicable = "not_applicable"
	StatusUnavailable   = "unavailable"
)

// Finding is one problem a check found, with a hint and, where a statement is
// precise and safe, the SQL that fixes it. FixSQL is never run by the platform.
type Finding struct {
	Check      string   `json:"check"`
	Category   string   `json:"category"`
	Level      string   `json:"level"`
	Object     string   `json:"object"`
	Index      string   `json:"index,omitempty"`
	Constraint string   `json:"constraint,omitempty"`
	Columns    []string `json:"columns,omitempty"`
	Role       string   `json:"role,omitempty"`
	Operation  string   `json:"operation,omitempty"`
	Message    string   `json:"message"`
	FixHint    string   `json:"fixHint"`
	FixSQL     string   `json:"fixSql,omitempty"`
}

// CheckStatus says whether a check ran, so a clean result is never confused
// with one that could not be read.
type CheckStatus struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
}

// Report is the whole answer for one project.
type Report struct {
	Engine   string        `json:"engine"`
	Findings []Finding     `json:"findings"`
	Checks   []CheckStatus `json:"checks"`
}

var levelRank = map[string]int{LevelError: 0, LevelWarning: 1, LevelInfo: 2}

// sortFindings orders by level, then check, object and index.
func sortFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if levelRank[a.Level] != levelRank[b.Level] {
			return levelRank[a.Level] < levelRank[b.Level]
		}
		if a.Check != b.Check {
			return a.Check < b.Check
		}
		if a.Object != b.Object {
			return a.Object < b.Object
		}
		return a.Index+a.Role+a.Operation < b.Index+b.Role+b.Operation
	})
}

func pgIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func mysqlIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// qualified quotes schema.name for the engine's SQL.
func qualified(engine, schemaName, name string) string {
	if engine == EngineMySQL {
		return mysqlIdent(schemaName) + "." + mysqlIdent(name)
	}
	return pgIdent(schemaName) + "." + pgIdent(name)
}
