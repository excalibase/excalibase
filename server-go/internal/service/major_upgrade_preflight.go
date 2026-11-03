package service

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/schema"
)

// The checks RDS runs before pg_upgrade (prepared transactions, reg* columns,
// half-dropped databases), plus extensions: a tenant can only install the
// allowlist, which every catalogue image carries, so anything else in a
// database is something the new major may not have.

// preflightDoneMarker ends a complete preflight output; without it the
// script stopped part way and nothing it printed is trusted.
const preflightDoneMarker = "preflight-done"

// preflightSeparator splits fields; database names may hold tabs and pipes.
const preflightSeparator = "\x1f"

// platformExtensions are installed by the platform, not by tenants.
var platformExtensions = map[string]bool{"plpgsql": true, "pg_stat_statements": true}

// superuserSessionOptions keep a superuser session in a tenant's database
// off the tenant's functions: a database-level search_path naming a tenant
// schema before pg_catalog would otherwise resolve even count() to theirs.
// Client options override database settings.
const superuserSessionOptions = "-c search_path=pg_catalog,pg_temp"

// preflightStatementTimeout bounds each check; GET runs them for any member.
const preflightStatementTimeout = "60s"

// preflightScript lists, as the superuser over the socket, what a major
// upgrade cannot carry. Every function is schema-qualified as well.
func preflightScript() string {
	return `set -eu
export PGOPTIONS='` + superuserSessionOptions + ` -c statement_timeout=` + preflightStatementTimeout + `'
sep=$(printf '\037')
q() { psql -X -A -t -q -v ON_ERROR_STOP=1 -U postgres -F "$sep" "$@"; }
q -d postgres -c "SELECT 'prepared', pg_catalog.count(*) FROM pg_catalog.pg_prepared_xacts"
q -d postgres -c "SELECT 'invalid', datname FROM pg_catalog.pg_database WHERE datconnlimit OPERATOR(pg_catalog.=) -2"
q -d postgres -c "SELECT datname FROM pg_catalog.pg_database WHERE datallowconn AND NOT datistemplate AND datconnlimit OPERATOR(pg_catalog.<>) -2" |
while IFS= read -r db; do
  q -d "$db" -c "SELECT 'ext', pg_catalog.current_database(), extname FROM pg_catalog.pg_extension" \
    -c "SELECT 'reg', pg_catalog.current_database(), pg_catalog.count(*) FROM pg_catalog.pg_attribute a
        JOIN pg_catalog.pg_class c ON c.oid OPERATOR(pg_catalog.=) a.attrelid
        JOIN pg_catalog.pg_namespace n ON n.oid OPERATOR(pg_catalog.=) c.relnamespace
        WHERE a.attnum OPERATOR(pg_catalog.>) 0 AND NOT a.attisdropped
          AND n.nspname OPERATOR(pg_catalog.<>) ALL (ARRAY['pg_catalog', 'information_schema']::pg_catalog.name[])
          AND a.atttypid OPERATOR(pg_catalog.=) ANY (ARRAY['pg_catalog.regproc'::pg_catalog.regtype, 'pg_catalog.regprocedure'::pg_catalog.regtype,
                             'pg_catalog.regoper'::pg_catalog.regtype, 'pg_catalog.regoperator'::pg_catalog.regtype,
                             'pg_catalog.regconfig'::pg_catalog.regtype, 'pg_catalog.regdictionary'::pg_catalog.regtype]::pg_catalog.oid[])"
done
echo ` + preflightDoneMarker + `
`
}

type preflightFindings struct {
	prepared         int
	invalidDatabases []string
	extensions       map[string][]string
	regColumns       map[string]int
}

var errPreflightIncomplete = errors.New("the database check did not finish")

func parsePreflight(out string) (preflightFindings, error) {
	findings := preflightFindings{extensions: map[string][]string{}, regColumns: map[string]int{}}
	done := false
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if line == preflightDoneMarker {
			done = true
			continue
		}
		if err := findings.add(strings.Split(line, preflightSeparator)); err != nil {
			return findings, err
		}
	}
	if !done {
		return findings, errPreflightIncomplete
	}
	return findings, nil
}

func (f *preflightFindings) add(fields []string) error {
	switch {
	case fields[0] == "prepared" && len(fields) == 2:
		n, err := strconv.Atoi(fields[1])
		f.prepared = n
		return err
	case fields[0] == "invalid" && len(fields) == 2:
		f.invalidDatabases = append(f.invalidDatabases, fields[1])
	case fields[0] == "ext" && len(fields) == 3:
		f.extensions[fields[1]] = append(f.extensions[fields[1]], fields[2])
	case fields[0] == "reg" && len(fields) == 3:
		n, err := strconv.Atoi(fields[2])
		f.regColumns[fields[1]] = n
		return err
	default:
		return fmt.Errorf("%w: unexpected line %q", errPreflightIncomplete, strings.Join(fields, " "))
	}
	return nil
}

// preflightBlockers turns findings into what the user must fix. available,
// when known, is what the target image can install.
func preflightBlockers(f preflightFindings, target string, available map[string]bool) []string {
	blockers := []string{}
	if f.prepared > 0 {
		blockers = append(blockers, fmt.Sprintf("%d prepared transaction(s) are open; commit or roll them back", f.prepared))
	}
	for _, db := range f.invalidDatabases {
		blockers = append(blockers, fmt.Sprintf("database %q was only partly dropped; drop it", db))
	}
	for _, db := range sortedKeys(f.extensions) {
		for _, ext := range f.extensions[db] {
			if blocker := extensionBlocker(db, ext, target, available); blocker != "" {
				blockers = append(blockers, blocker)
			}
		}
	}
	for _, db := range sortedKeys(f.regColumns) {
		if f.regColumns[db] > 0 {
			blockers = append(blockers, fmt.Sprintf("database %q has %d column(s) of a reg* type (regproc, regoper, regconfig…), which an upgrade cannot carry", db, f.regColumns[db]))
		}
	}
	return blockers
}

func extensionBlocker(db, ext, target string, available map[string]bool) string {
	if !platformExtensions[ext] && !schema.IsExtensionAllowed(ext) {
		return fmt.Sprintf("database %q uses the extension %q, which is not offered on newer versions; drop it first", db, ext)
	}
	if available != nil && !available[ext] {
		return fmt.Sprintf("database %q uses the extension %q, which Postgres %s does not have here; drop it first", db, ext, target)
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func parseAvailableExtensions(out string) map[string]bool {
	available := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if name := strings.TrimSpace(line); name != "" {
			available[name] = true
		}
	}
	return available
}
