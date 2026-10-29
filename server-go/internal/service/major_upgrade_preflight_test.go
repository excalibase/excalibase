package service

import (
	"strings"
	"testing"
)

func preflightLines(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

const fieldSep = "\x1f"

func TestParsePreflight_ReadsEveryKindOfFinding(t *testing.T) {
	out := preflightLines(
		"prepared"+fieldSep+"2",
		"invalid"+fieldSep+"half dropped",
		"ext"+fieldSep+"app"+fieldSep+"plpgsql",
		"ext"+fieldSep+"app"+fieldSep+"vector",
		"ext"+fieldSep+"app"+fieldSep+"dblink",
		"reg"+fieldSep+"app"+fieldSep+"3",
		"reg"+fieldSep+"other"+fieldSep+"0",
		preflightDoneMarker,
	)
	findings, err := parsePreflight(out)
	if err != nil {
		t.Fatal(err)
	}
	if findings.prepared != 2 || len(findings.invalidDatabases) != 1 || findings.invalidDatabases[0] != "half dropped" {
		t.Fatalf("findings = %+v", findings)
	}
	if len(findings.extensions["app"]) != 3 || findings.regColumns["app"] != 3 || findings.regColumns["other"] != 0 {
		t.Fatalf("findings = %+v", findings)
	}
}

func TestParsePreflight_AnOutputThatDidNotFinishIsAnError(t *testing.T) {
	if _, err := parsePreflight(preflightLines("prepared" + fieldSep + "0")); err == nil {
		t.Fatal("an output without the done marker was accepted")
	}
	if _, err := parsePreflight(preflightLines("prepared"+fieldSep+"x", preflightDoneMarker)); err == nil {
		t.Fatal("a count that is not a number was accepted")
	}
	if _, err := parsePreflight(preflightLines("mystery"+fieldSep+"1", preflightDoneMarker)); err == nil {
		t.Fatal("an unknown line was accepted")
	}
}

func TestPreflightBlockers(t *testing.T) {
	findings := preflightFindings{
		prepared:         1,
		invalidDatabases: []string{"gone"},
		extensions: map[string][]string{
			"app":   {"plpgsql", "pg_stat_statements", "vector", "postgis", "dblink"},
			"other": {"plpgsql", "file_fdw"},
		},
		regColumns: map[string]int{"app": 2},
	}
	blockers := strings.Join(preflightBlockers(findings, "17", nil), " | ")
	for _, want := range []string{
		"prepared transaction",
		`"gone"`,
		`"dblink"`, `"file_fdw"`,
		"reg",
	} {
		if !strings.Contains(blockers, want) {
			t.Errorf("blockers %q miss %q", blockers, want)
		}
	}
	for _, allowed := range []string{`"vector"`, `"postgis"`, `"plpgsql"`, `"pg_stat_statements"`} {
		if strings.Contains(blockers, allowed) {
			t.Errorf("blockers name an extension every image carries: %s", allowed)
		}
	}
}

func TestPreflightBlockers_ATargetImageWithoutAnExtensionBlocksIt(t *testing.T) {
	findings := preflightFindings{extensions: map[string][]string{"app": {"plpgsql", "vector", "citext"}}, regColumns: map[string]int{}}
	available := map[string]bool{"plpgsql": true, "citext": true}
	blockers := preflightBlockers(findings, "17", available)
	if len(blockers) != 1 || !strings.Contains(blockers[0], `"vector"`) || !strings.Contains(blockers[0], "17") {
		t.Fatalf("blockers = %v", blockers)
	}
}

func TestPreflightBlockers_NoneForAPlainDatabase(t *testing.T) {
	findings := preflightFindings{extensions: map[string][]string{"app": {"plpgsql"}}, regColumns: map[string]int{"app": 0}}
	if blockers := preflightBlockers(findings, "17", nil); len(blockers) != 0 {
		t.Fatalf("blockers = %v", blockers)
	}
}

func TestPreflightScriptRunsAsTheSuperuserOverTheSocketOnly(t *testing.T) {
	script := preflightScript()
	for _, want := range []string{"-U postgres", "pg_prepared_xacts", "datconnlimit OPERATOR(pg_catalog.=) -2", "pg_extension", "regproc", preflightDoneMarker, "set -eu"} {
		if !strings.Contains(script, want) {
			t.Errorf("script misses %q", want)
		}
	}
	if strings.Contains(script, "-h ") {
		t.Error("the preflight connects over the network")
	}
}

func TestParseAvailableExtensions(t *testing.T) {
	available := parseAvailableExtensions(" plpgsql\nvector\n\ncitext \n")
	if len(available) != 3 || !available["vector"] || !available["citext"] {
		t.Fatalf("available = %v", available)
	}
}

// A tenant's database-level search_path must not turn the superuser's checks
// into calls of the tenant's own functions.
func TestSuperuserScriptsPinTheSearchPath(t *testing.T) {
	for name, script := range map[string]string{"preflight": preflightScript(), "after steps": afterUpgradeScript} {
		if !strings.Contains(script, "search_path=pg_catalog,pg_temp") {
			t.Errorf("%s does not pin search_path", name)
		}
		for _, bare := range []string{" count(", " current_database(", " format(", "(format("} {
			if strings.Contains(script, bare) {
				t.Errorf("%s calls %q unqualified", name, strings.TrimSpace(bare))
			}
		}
	}
	if !strings.Contains(preflightScript(), "statement_timeout="+preflightStatementTimeout) {
		t.Error("the preflight runs without a time limit")
	}
}
