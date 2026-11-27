package planbudget

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
)

// script answers the gate's queries from a table, so the gate runs on a real *sql.DB with no database behind it.
type script struct {
	mu      sync.Mutex
	asked   []string
	answers []answer
}

type answer struct {
	match string
	cols  []string
	rows  [][]driver.Value
	err   error
}

func (s *script) reply(query string) (*answer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, query)
	for i := range s.answers {
		if strings.Contains(query, s.answers[i].match) {
			if s.answers[i].err != nil {
				return nil, s.answers[i].err
			}
			return &s.answers[i], nil
		}
	}
	return nil, fmt.Errorf("unscripted query: %s", query)
}

func (s *script) askedAbout(fragment string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, q := range s.asked {
		if strings.Contains(q, fragment) {
			return true
		}
	}
	return false
}

type scriptDriver struct{}

var scripts sync.Map

func init() { sql.Register("planbudget-script", scriptDriver{}) }

func (scriptDriver) Open(name string) (driver.Conn, error) {
	s, _ := scripts.Load(name)
	return &scriptConn{script: s.(*script)}, nil
}

type scriptConn struct{ script *script }

func (c *scriptConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not used") }
func (c *scriptConn) Close() error                        { return nil }
func (c *scriptConn) Begin() (driver.Tx, error)           { return nil, errors.New("not used") }

func (c *scriptConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if _, err := c.script.reply(query); err != nil {
		return nil, err
	}
	return driver.ResultNoRows, nil
}

func (c *scriptConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	found, err := c.script.reply(query)
	if err != nil {
		return nil, err
	}
	return &scriptRows{cols: found.cols, rows: found.rows}, nil
}

type scriptRows struct {
	cols []string
	rows [][]driver.Value
	next int
}

func (r *scriptRows) Columns() []string { return r.cols }
func (r *scriptRows) Close() error      { return nil }
func (r *scriptRows) Next(dest []driver.Value) error {
	if r.next >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.next])
	r.next++
	return nil
}

var testSeq int

func openScript(t *testing.T, answers ...answer) (*sql.DB, *script) {
	t.Helper()
	testSeq++
	name := fmt.Sprintf("%s-%d", t.Name(), testSeq)
	s := &script{answers: answers}
	scripts.Store(name, s)
	db, err := sql.Open("planbudget-script", name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(); scripts.Delete(name) })
	return db, s
}

var (
	databaseCols = []string{"project_id", "project_name", "tier", "status", "no_database", "storage_size", "cpu", "mem", "disk", "copies", "has_runtime"}
	appCols      = []string{"project_id", "doc", "deploying"}
	okLock       = answer{match: "pg_advisory_xact_lock"}
)

func tierIs(tier string) answer {
	return answer{match: "SELECT tier FROM orgs", cols: []string{"tier"}, rows: [][]driver.Value{{tier}}}
}

func databases(rows ...[]driver.Value) answer {
	return answer{match: "FROM database_instances", cols: databaseCols, rows: rows}
}

func apps(rows ...[]driver.Value) answer {
	return answer{match: "FROM apps a", cols: appCols, rows: rows}
}

// a running one-copy database of 500m, 512 MiB and 1 GiB.
func runningDatabase(id string) []driver.Value {
	return []driver.Value{id, "name-of-" + id, "FREE", "ACTIVE", false, "", int64(500), int64(512 << 20), int64(1 << 30), int64(1), false}
}

var tinyPlans = config.PlanBudgetConfig{
	Free:     config.PlanNumbers{CPUMilli: 1000, MemoryMiB: 1024, DiskGiB: 4, MaxServices: 3},
	Standard: config.PlanNumbers{CPUMilli: 4000, MemoryMiB: 8192, DiskGiB: 50, MaxServices: 10},
}

func gateOn() *Gate { return NewGate(func(context.Context) bool { return true }, tinyPlans) }

func grows(size Size) Delta {
	return func(context.Context) (Size, error) { return size, nil }
}

func TestAStepThatFitsUnderThePlanIsAdmitted(t *testing.T) {
	db, script := openScript(t, okLock, tierIs("FREE"), databases(runningDatabase("p1")), apps())
	if err := gateOn().Admit(context.Background(), db, "org-1", grows(Size{CPUMilli: 400, Services: 1})); err != nil {
		t.Fatalf("500m used of 1000m and 400m asked: %v", err)
	}
	if !script.askedAbout("pg_advisory_xact_lock") {
		t.Fatal("a step takes the org lock before it reads anything")
	}
}

func TestAStepThatPassesThePlanIsRefusedWithTheNumbers(t *testing.T) {
	db, _ := openScript(t, okLock, tierIs("FREE"), databases(runningDatabase("p1")), apps())
	err := gateOn().Admit(context.Background(), db, "org-1", grows(Size{CPUMilli: 501}))
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Dimension != "CPU" || refusal.Left.CPUMilli != 500 || refusal.Need.CPUMilli != 501 {
		t.Fatalf("one milli over the plan must be refused: %v", err)
	}
	if err := gateOn().Admit(context.Background(), db, "org-1", grows(Size{CPUMilli: 500})); err != nil {
		t.Fatalf("exactly what is left must fit: %v", err)
	}
}

func TestTheStandardPlanHasItsOwnCeiling(t *testing.T) {
	db, _ := openScript(t, okLock, tierIs("standard"), databases(runningDatabase("p1")), apps())
	if err := gateOn().Admit(context.Background(), db, "org-1", grows(Size{CPUMilli: 3000})); err != nil {
		t.Fatalf("a standard org may hold 4000m: %v", err)
	}
}

func TestAGateThatIsOffOrMissingAdmitsWithoutTouchingTheDatabase(t *testing.T) {
	db, script := openScript(t)
	huge := grows(Size{CPUMilli: 1 << 40})
	off := NewGate(func(context.Context) bool { return false }, tinyPlans)
	for name, gate := range map[string]*Gate{"off": off, "nil": nil, "zero": {}} {
		if err := gate.Admit(context.Background(), db, "org-1", huge); err != nil {
			t.Errorf("%s gate refused: %v", name, err)
		}
		if gate.Active(context.Background()) {
			t.Errorf("%s gate reports active", name)
		}
	}
	if err := gateOn().Admit(context.Background(), db, "", huge); err != nil {
		t.Fatalf("a step with no org has no plan: %v", err)
	}
	if len(script.asked) != 0 {
		t.Fatalf("an inactive gate must not query: %v", script.asked)
	}
}

func TestAShrinkingStepPassesWithoutReadingUsage(t *testing.T) {
	db, script := openScript(t, okLock)
	if err := gateOn().Admit(context.Background(), db, "org-1", grows(Size{CPUMilli: -500, MemoryBytes: -1})); err != nil {
		t.Fatal(err)
	}
	if script.askedAbout("FROM orgs") || script.askedAbout("database_instances") {
		t.Fatalf("a shrink reads no usage: %v", script.asked)
	}
}

func TestAPlanWithNoBudgetAndAnOrgWithNoRowAreNotLimited(t *testing.T) {
	huge := grows(Size{CPUMilli: 1 << 40})
	enterprise, _ := openScript(t, okLock, tierIs("ENTERPRISE"))
	if err := gateOn().Admit(context.Background(), enterprise, "org-1", huge); err != nil {
		t.Fatalf("a plan with no budget: %v", err)
	}
	missing, _ := openScript(t, okLock, answer{match: "SELECT tier FROM orgs", cols: []string{"tier"}})
	if err := gateOn().Admit(context.Background(), missing, "org-1", huge); err != nil {
		t.Fatalf("an org with no row: %v", err)
	}
}

func TestEveryStoreErrorReachesTheCaller(t *testing.T) {
	boom := errors.New("connection reset")
	grow := grows(Size{CPUMilli: 1})
	cases := map[string][]answer{
		"lock":       {{match: "pg_advisory_xact_lock", err: boom}},
		"tier":       {okLock, {match: "SELECT tier FROM orgs", err: boom}},
		"databases":  {okLock, tierIs("FREE"), {match: "FROM database_instances", err: boom}},
		"apps":       {okLock, tierIs("FREE"), databases(), {match: "FROM apps a", err: boom}},
		"scan":       {okLock, tierIs("FREE"), {match: "FROM database_instances", cols: databaseCols, rows: [][]driver.Value{{"p", "FREE", "ACTIVE", "not a bool", "", int64(0), int64(0), int64(0), int64(1), false}}}},
		"app scan":   {okLock, tierIs("FREE"), databases(), {match: "FROM apps a", cols: appCols, rows: [][]driver.Value{{[]byte(`{}`), "not a bool"}}}},
		"unreadable": {okLock, tierIs("FREE"), databases(), apps([]driver.Value{"p1", []byte(`not json`), false})},
	}
	for name, answers := range cases {
		db, _ := openScript(t, answers...)
		err := gateOn().Admit(context.Background(), db, "org-1", grow)
		if err == nil {
			t.Errorf("%s: the error was swallowed", name)
			continue
		}
		var refusal *Refusal
		if errors.As(err, &refusal) {
			t.Errorf("%s: a store failure must not look like a refusal: %v", name, err)
		}
	}
	db, _ := openScript(t, okLock)
	failing := func(context.Context) (Size, error) { return Size{}, boom }
	if err := gateOn().Admit(context.Background(), db, "org-1", failing); !errors.Is(err, boom) {
		t.Fatalf("the delta's own error: %v", err)
	}
}

func TestADatabaseWhoseSizeWasNeverRecordedBlocksGrowthForItsOrg(t *testing.T) {
	unrecorded := []driver.Value{"p-unread", "name-p-unread", "FREE", "ACTIVE", false, "", int64(0), int64(0), int64(0), int64(0), false}
	db, _ := openScript(t, okLock, tierIs("FREE"), databases(runningDatabase("p1"), unrecorded), apps())
	err := gateOn().Admit(context.Background(), db, "org-1", grows(Size{CPUMilli: 1}))
	if !errors.Is(err, ErrUnrecordedSize) || !strings.Contains(err.Error(), "p-unread") {
		t.Fatalf("growth must fail closed and name the project: %v", err)
	}
}

func TestDatabasesThatHoldNothingNeverBlockGrowth(t *testing.T) {
	deleting := []driver.Value{"p-gone", "name-p-gone", "FREE", "DELETING", false, "", int64(0), int64(0), int64(0), int64(0), false}
	noDatabase := []driver.Value{"p-none", "name-p-none", "FREE", "ACTIVE", true, "", int64(0), int64(0), int64(0), int64(0), false}
	db, _ := openScript(t, okLock, tierIs("FREE"), databases(deleting, noDatabase), apps())
	if err := gateOn().Admit(context.Background(), db, "org-1", grows(Size{CPUMilli: 1000})); err != nil {
		t.Fatalf("a deleting database and a project with no database hold nothing: %v", err)
	}
}

func TestUsageAddsTheDatabaseItsDiskItsFunctionsRuntimeAndItsApps(t *testing.T) {
	withRuntime := []driver.Value{"p1", "name-p1", "STANDARD", "ACTIVE", false, "2Gi", int64(100), int64(64 << 20), int64(1 << 30), int64(2), true}
	app := []byte(`{"tier":"FREE","replicas":1,"status":"ACTIVE","disk":{"size":"1Gi"}}`)
	db, _ := openScript(t, databases(withRuntime), apps([]driver.Value{"p1", app, false}, []driver.Value{"p1", app, true}))
	got, err := OrgUsage(context.Background(), db, "org-1")
	if err != nil {
		t.Fatal(err)
	}
	database := Size{CPUMilli: 200, MemoryBytes: 128 << 20, DiskBytes: 4 << 30, Services: 1}
	runtime := ReservedFunctions("STANDARD")
	oneApp := Size{CPUMilli: 250, MemoryBytes: 256 << 20, DiskBytes: 1 << 30, Services: 1}
	want := database.Add(runtime).Add(oneApp).Add(oneApp)
	if got != want {
		t.Fatalf("usage %+v, want %+v", got, want)
	}
}

func TestAnAppWithADeployInFlightIsCountedAsRunning(t *testing.T) {
	stopped := []byte(`{"tier":"FREE","replicas":1,"status":"PAUSED"}`)
	db, _ := openScript(t, databases(), apps([]driver.Value{"p1", stopped, true}))
	got, err := OrgUsage(context.Background(), db, "org-1")
	if err != nil || got.CPUMilli != 250 {
		t.Fatalf("a deploy of a stopped app holds what it will run at: %+v %v", got, err)
	}
	idle, _ := openScript(t, databases(), apps([]driver.Value{"p1", stopped, false}))
	if got, _ := OrgUsage(context.Background(), idle, "org-1"); got.CPUMilli != 0 {
		t.Fatalf("a stopped app with no deploy holds no CPU: %+v", got)
	}
}

func TestCeilingReadsTheTierCaseInsensitively(t *testing.T) {
	gate := gateOn()
	free, ok := gate.Ceiling("free")
	if !ok || free != (Size{CPUMilli: 1000, MemoryBytes: 1024 << 20, DiskBytes: 4 << 30, Services: 3}) {
		t.Fatalf("%+v %v", free, ok)
	}
	if standard, ok := gate.Ceiling("Standard"); !ok || standard.CPUMilli != 4000 {
		t.Fatalf("%+v %v", standard, ok)
	}
	if _, ok := gate.Ceiling("ENTERPRISE"); ok {
		t.Fatal("enterprise has no budget")
	}
}

func TestOrgServicesNamesEachServiceByKindAndProject(t *testing.T) {
	withRuntime := []driver.Value{"p1", "shop", "STANDARD", "ACTIVE", false, "", int64(100), int64(64 << 20), int64(1 << 30), int64(1), true}
	unrecorded := []driver.Value{"p2", "blog", "FREE", "ACTIVE", false, "", int64(0), int64(0), int64(0), int64(0), false}
	app := []byte(`{"name":"web","tier":"FREE","replicas":1,"status":"ACTIVE"}`)
	db, _ := openScript(t, databases(withRuntime, unrecorded), apps([]driver.Value{"p1", app, false}, []driver.Value{"p1", []byte(`{"tier":"FREE","replicas":0,"status":"ACTIVE"}`), false}))
	got, err := OrgServices(context.Background(), db, "org-1")
	if err != nil {
		t.Fatal(err)
	}
	kinds := make([]string, len(got))
	for i, service := range got {
		kinds[i] = service.Kind + ":" + service.Name + ":" + service.ProjectID
	}
	want := []string{"database:shop:p1", "functions:shop:p1", "database:blog:p2", "app:web:p1", "app::p1"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("services %v, want %v", kinds, want)
	}
	if got[0].Unrecorded || !got[2].Unrecorded {
		t.Fatalf("only the database with no recorded size is marked unrecorded: %+v", got)
	}
	if got[1].Size != ReservedFunctions("STANDARD") {
		t.Fatalf("the functions runtime holds its plan's pod: %+v", got[1].Size)
	}
}

func TestAnAppRecordThatIsNotJSONHasNoNameButStillCounts(t *testing.T) {
	if appName([]byte("not json")) != "" || appName([]byte(`{"name":"web"}`)) != "web" {
		t.Fatal("appName")
	}
}

func TestTheReportShowsThePlanWhatIsUsedAndWhatIsLeft(t *testing.T) {
	db, _ := openScript(t, tierIs("FREE"), databases(runningDatabase("p1")), apps())
	report, err := gateOn().Report(context.Background(), db, "org-1")
	if err != nil {
		t.Fatal(err)
	}
	if !report.Enabled || report.Plan != "FREE" || report.Used.CPUMilli != 500 || report.Left.CPUMilli != 500 || report.Budget.CPUMilli != 1000 || len(report.Services) != 1 {
		t.Fatalf("%+v", report)
	}
}

func TestTheReportOfAPlanWithNoCeilingHasUsageAndNoLimit(t *testing.T) {
	db, _ := openScript(t, tierIs("ENTERPRISE"), databases(), apps())
	report, err := gateOn().Report(context.Background(), db, "org-1")
	if err != nil || !report.Budget.IsZero() || !report.Left.IsZero() || report.Services == nil || len(report.Services) != 0 {
		t.Fatalf("%+v %v", report, err)
	}
}

func TestTheReportIsEmptyWhileTheBudgetIsOff(t *testing.T) {
	db, script := openScript(t)
	off := NewGate(func(context.Context) bool { return false }, tinyPlans)
	report, err := off.Report(context.Background(), db, "org-1")
	if err != nil || report.Enabled || report.Services == nil || len(script.asked) != 0 {
		t.Fatalf("%+v %v %v", report, err, script.asked)
	}
}

func TestTheReportPassesOnEveryStoreError(t *testing.T) {
	boom := errors.New("down")
	for name, answers := range map[string][]answer{
		"tier":      {{match: "SELECT tier FROM orgs", err: boom}},
		"databases": {tierIs("FREE"), {match: "FROM database_instances", err: boom}},
	} {
		db, _ := openScript(t, answers...)
		if _, err := gateOn().Report(context.Background(), db, "org-1"); !errors.Is(err, boom) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
