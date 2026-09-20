package scheduler

import (
	"database/sql"
	"encoding/json"
	"regexp"
)

// Everything in the scheduler tables is tenant-written: the project owns its
// database and holds its credentials, so it can put any value in any column.
// This file is where that content stops being input and becomes either data
// the platform may use or a refusal.

// Reasons a row is closed without running. They are fixed platform text so
// nothing a tenant wrote is ever echoed back into the row.
const (
	rejectForeignProject  = "refused: row names a project other than the one that owns this database"
	rejectBadIdentifier   = "refused: module or export name is not a plain identifier"
	rejectBadArgs         = "refused: args are not JSON within the size limit"
	rejectUnknownFunction = "refused: no such function is deployed for this project"
	rejectMalformedRow    = "refused: row columns are missing or of the wrong type"
)

// DefaultMaxArgsBytes matches the public invoke body limit (1 MiB): a
// scheduled call may not carry more than a direct one.
const DefaultMaxArgsBytes = 1024 * 1024

// DefaultProjectConcurrency caps in-flight invocations for one project.
const DefaultProjectConcurrency = 4

// modulePattern is the shape of a function id: segments of letters, digits,
// underscore, dash or dot, optionally nested with "/". No leading slash, no
// "..", nothing that could climb out of the runtime's id space.
var modulePattern = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.\-]*(/[a-zA-Z0-9_][a-zA-Z0-9_.\-]*)*$`)

// exportPattern is a plain JavaScript export identifier.
var exportPattern = regexp.MustCompile(`^[a-zA-Z_$][a-zA-Z0-9_$]{0,63}$`)

const maxModuleNameLen = 128

func validModuleName(name string) bool {
	if name == "" || len(name) > maxModuleNameLen {
		return false
	}
	for _, segment := range splitPath(name) {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return modulePattern.MatchString(name)
}

func validExportName(name string) bool {
	return exportPattern.MatchString(name)
}

// splitPath splits a module name on "/" without pulling in path semantics.
func splitPath(name string) []string {
	var out []string
	start := 0
	for i := 0; i < len(name); i++ {
		if name[i] == '/' {
			out = append(out, name[start:i])
			start = i + 1
		}
	}
	return append(out, name[start:])
}

// FunctionRegistry is the platform's record of what it deployed. The sweep
// asks it before running anything: a row may only name a module the platform
// itself put in the project's runtime.
type FunctionRegistry interface {
	HasFunction(projectID, moduleName string) (bool, error)
}

// scanPendingRow reads one claimed row defensively. Every column is scanned
// as nullable so a NULL or wrongly-typed value closes that row instead of
// failing the whole claim for the project.
func scanPendingRow(rs *sql.Rows) (pendingRow, error) {
	var (
		id, projectID, moduleName, exportName sql.NullString
		args                                  []byte
		attempts                              sql.NullInt64
	)
	if err := rs.Scan(&id, &projectID, &moduleName, &exportName, &args, &attempts); err != nil {
		return pendingRow{}, err
	}
	row := pendingRow{
		ID:         id.String,
		ProjectID:  projectID.String,
		ModuleName: moduleName.String,
		ExportName: exportName.String,
		Args:       json.RawMessage(args),
		Attempts:   int(attempts.Int64),
	}
	if !id.Valid || !projectID.Valid || !moduleName.Valid || !exportName.Valid || args == nil {
		row.Invalid = true
	}
	return row, nil
}

// clampAttempts keeps the platform's retry budget the platform's: a count
// the tenant wrote cannot go below zero (extra retries) and cannot exceed
// the limit (which would only end the row sooner, but is still not theirs
// to decide).
func clampAttempts(attempts, maxAttempts int) int {
	if attempts < 0 {
		return 0
	}
	if attempts > maxAttempts {
		return maxAttempts
	}
	return attempts
}
