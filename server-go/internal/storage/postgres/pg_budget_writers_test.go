package postgres

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Every function that writes a service's size or running state either goes
// through the plan budget gate or is named here with the reason it can never
// grow what an org reserves. A new writer that is in neither list fails this
// test, so a growth path cannot be added without a decision about admission.
var admittedWriters = map[string]string{
	"storage/postgres/pg_instances.go:insertInstance":                    "admitted by admitInsert, in the create transaction",
	"storage/postgres/pg_instances.go:updateInstance":                    "admitted by updateAdmitted and UpdateIfStatusWithinOrgLimit",
	"storage/postgres/pg_instances.go:UpdateStorageSizeIfStatus":         "admits the disk it grows",
	"storage/postgres/pg_instances_no_database.go:RecordDatabaseChoices": "admits the database being added",
	"storage/postgres/pg_budget.go:EnsureFunctionsRuntime":               "admits the functions runtime",
	"apphost/postgres_store.go:Create":                                   "admits the new app",
	"apphost/postgres_store.go:Update":                                   "admits a replica, tier or disk change",
	"apphost/postgres_store.go:Transition":                               "admits a status that starts the app again",
	"apphost/postgres_deploy_store.go:RecordResize":                      "admits the size a plan change gives the app",
}

var neverGrowWriters = map[string]string{
	"storage/postgres/pg_instances.go:RecordRollbackRetry":            "writes the rollback log and failure text",
	"storage/postgres/pg_instances.go:UpdateParametersIfStatus":       "writes Postgres parameters, not size",
	"storage/postgres/pg_instances.go:BeginDeletion":                  "moves a project towards deletion, which releases",
	"storage/postgres/pg_instances.go:RecordDeletionFailure":          "keeps a project in a deletion status",
	"storage/postgres/pg_instances.go:RecordRestoreInterrupted":       "writes a step and a reason",
	"storage/postgres/pg_instances.go:RecordPauseAttempt":             "counts a pause attempt",
	"storage/postgres/pg_instances_no_database.go:MarkDatabaseAdded":  "flips a flag; RecordDatabaseChoices already admitted and recorded the size",
	"storage/postgres/pg_major_upgrades.go:UpdateMajorUpgrade":        "records an upgrade run's status on a running database",
	"storage/postgres/pg_major_upgrades.go:MarkMajorUpgradeConverted": "records the new version on a running database",
	"storage/postgres/pg_budget.go:recordReservation":                 "records the size an admission just granted",
	"storage/postgres/pg_budget.go:releaseSettledReservation":         "gives a failed add's reservation back",
	"storage/postgres/pg_budget.go:backfillOne":                       "boot backfill: records sizes already running, touches no workload",
	"storage/postgres/pg_budget.go:MarkFunctionsRuntimes":             "boot backfill: records runtimes already running",
	"apphost/postgres_store.go:RecordLifecycleFailure":                "writes a failure note into the record",
	"apphost/postgres_deploy_store.go:Finish":                         "records the status a deploy observed; the deploy was admitted when it started",
	"apphost/postgres_suspension.go:RecordSuspension":                 "writes the suspension note; a suspension stops the app",
	"apphost/postgres_store.go:RecordImageWatch":                      "writes what the image watcher saw",
}

var writerStatements = []string{"INSERT INTO database_instances", "UPDATE database_instances", "INSERT INTO apps", "UPDATE apps"}

func TestEveryWriterOfAServiceIsAdmittedOrCannotGrowIt(t *testing.T) {
	found := map[string]bool{}
	for _, dir := range []string{".", "../../apphost"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			for _, name := range writersIn(t, file) {
				found[trimPath(file)+":"+name] = true
			}
		}
	}
	var unknown []string
	for key := range found {
		if admittedWriters[key] == "" && neverGrowWriters[key] == "" {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		t.Fatalf("these functions write a service's size or state with no admission decision; admit them or name why they cannot grow: %v", unknown)
	}
	for _, list := range []map[string]string{admittedWriters, neverGrowWriters} {
		for key := range list {
			if !found[key] {
				t.Errorf("%s is listed but no longer writes a service; remove it", key)
			}
		}
	}
}

func trimPath(file string) string {
	file = filepath.ToSlash(filepath.Clean(file))
	if strings.HasPrefix(file, "../../") {
		return strings.TrimPrefix(file, "../../")
	}
	return "storage/postgres/" + file
}

func writersIn(t *testing.T, file string) []string {
	t.Helper()
	source, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), file, source, 0)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			for _, statement := range writerStatements {
				if strings.Contains(literal.Value, statement) {
					names = append(names, function.Name.Name)
					return false
				}
			}
			return true
		})
	}
	return names
}

// The writers that admit must really call the gate, and so must the steps that
// grow a service without writing its row themselves.
var mustAdmit = map[string]bool{
	"storage/postgres/pg_instances.go:UpdateStorageSizeIfStatus":          true,
	"storage/postgres/pg_instances.go:updateAdmitted":                     true,
	"storage/postgres/pg_instances.go:Create":                             true,
	"storage/postgres/pg_instances_limit.go:CreateWithinOrgLimit":         true,
	"storage/postgres/pg_instances_limit.go:UpdateIfStatusWithinOrgLimit": true,
	"storage/postgres/pg_instances_no_database.go:RecordDatabaseChoices":  true,
	"storage/postgres/pg_budget.go:EnsureFunctionsRuntime":                true,
	"storage/postgres/pg_budget.go:ReserveDatabaseSize":                   true,
	"apphost/postgres_store.go:Create":                                    true,
	"apphost/postgres_store.go:Update":                                    true,
	"apphost/postgres_store.go:Transition":                                true,
	"apphost/postgres_deploy_store.go:Create":                             true,
	"apphost/postgres_deploy_store.go:RecordResize":                       true,
}

var admissionCalls = map[string]bool{"Admit": true, "admitApp": true, "admitInsert": true, "admitStatusChange": true, "admitDiskChange": true, "admitDeploy": true, "admitResize": true, "updateAdmitted": true}

func TestEveryAdmittingWriterReallyCallsTheGate(t *testing.T) {
	calls := map[string]bool{}
	for _, dir := range []string{".", "../../apphost"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			for name := range callersIn(t, file) {
				calls[trimPath(file)+":"+name] = true
			}
		}
	}
	for key := range mustAdmit {
		if !calls[key] {
			t.Errorf("%s no longer calls the plan budget gate", key)
		}
	}
}

func callersIn(t *testing.T, file string) map[string]bool {
	t.Helper()
	source, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), file, source, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fn := call.Fun.(type) {
			case *ast.SelectorExpr:
				if admissionCalls[fn.Sel.Name] {
					out[function.Name.Name] = true
				}
			case *ast.Ident:
				if admissionCalls[fn.Name] {
					out[function.Name.Name] = true
				}
			}
			return true
		})
	}
	return out
}
