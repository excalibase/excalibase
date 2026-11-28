//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/credits"
	"github.com/excalibase/provisioning-poc/internal/creditzero"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/planbudget"
)

// zeroGate: a Free plan of 4000m / 8 GiB RAM / 100 GiB disk, whose free allowance is 1000m / 1 GiB / 20 GiB,
// with the credit check wired as the platform wires it.
func zeroGate() *planbudget.Gate {
	free := config.PlanNumbers{
		CPUMilli: 4000, MemoryMiB: 8192, DiskGiB: 100, MaxProjects: 50, MaxServices: 50,
		AllowanceCPUMilli: 1000, AllowanceMemoryMiB: 1024, AllowanceDiskGiB: 20,
	}
	gate := planbudget.NewGate(func(context.Context) bool { return true }, config.PlanBudgetConfig{Free: free, Standard: free})
	gate.SetZeroCheck(planbudget.CreditUsedUp)
	return gate
}

// zeroStore is a store with the credit gate on, and a free org funded then drained to nothing.
func zeroStore(t *testing.T, person string) (*Store, string) {
	t.Helper()
	store := testStore(t)
	store.SetPlanBudget(zeroGate())
	return store, creditPerson(t, store, person)
}

func fund(t *testing.T, store *Store, org string, dollars int64) {
	t.Helper()
	fundAt(t, store, org, fmt.Sprintf("fund-%s-%d-%d", org, dollars, time.Now().UnixNano()), credits.FromDollars(dollars), nil, databaseNow(t, store))
}

// drain takes the whole balance back, leaving an account at 0.
func drain(t *testing.T, store *Store, org string) {
	t.Helper()
	account, err := store.CreditAccount(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}
	if account.Balance == 0 {
		return
	}
	_, err = store.AdjustCredit(context.Background(), credits.Adjustment{
		OrgID: org, Amount: -account.Balance, Actor: "admin-1", Reason: "drain credit for a test",
		IdempotencyKey: fmt.Sprintf("drain-%s-%d", org, time.Now().UnixNano()),
		Audit:          credits.AuditRecord{UserID: "admin-1", Action: "admin.credit_adjust", Resource: "org", Details: "drain"},
	}, databaseNow(t, store))
	if err != nil {
		t.Fatalf("drain %s: %v", org, err)
	}
}

func addApp(t *testing.T, store *Store, project, id, status string, replicas int, age time.Duration) {
	t.Helper()
	doc := fmt.Sprintf(`{"tier":"FREE","replicas":%d,"status":%q}`, replicas, status)
	if _, err := store.db.Exec(`INSERT INTO apps (project_id, id, name, status, version, doc, created_at) VALUES ($1, $2, $2, $3, 1, $4::jsonb, now() - $5::interval)`,
		project, id, status, doc, fmt.Sprintf("%d seconds", int(age.Seconds()))); err != nil {
		t.Fatalf("seed app %s: %v", id, err)
	}
}

func ageDatabase(t *testing.T, store *Store, project string, age time.Duration) {
	t.Helper()
	if _, err := store.db.Exec(`UPDATE database_instances SET created_at = now() - $2::interval WHERE project_id = $1`, project, fmt.Sprintf("%d seconds", int(age.Seconds()))); err != nil {
		t.Fatal(err)
	}
}

func isCreditRefusal(err error) bool {
	var refusal *planbudget.Refusal
	return errors.As(err, &refusal) && refusal.CreditExhausted
}

func TestOrgsListsEveryCreditAccountWithWhetherItIsAtZero(t *testing.T) {
	store, spent := zeroStore(t, "zs")
	fund(t, store, spent, 5)
	drain(t, store, spent)
	funded := creditPerson(t, store, "zf")
	fund(t, store, funded, 5)
	paid := orgOf(t, store, "zp")
	if _, err := store.db.Exec(`UPDATE orgs SET tier = 'STANDARD' WHERE id = $1`, paid); err != nil {
		t.Fatal(err)
	}
	fund(t, store, paid, 5)
	drain(t, store, paid)
	creditPerson(t, store, "zn") // an org with no account at all

	orgs, err := NewCreditZeroStore(store).Orgs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, org := range orgs {
		got[org.OrgID] = org.AtZero
	}
	want := map[string]bool{spent: true, funded: false, paid: false}
	if len(got) != len(want) || got[spent] != true || got[funded] != false || got[paid] != false {
		t.Fatalf("a FREE org with an empty account is at zero; funded, paid and account-less orgs never are: %v", got)
	}
}

func TestOrgsReportsTheGraceStartAndASuspendedOwner(t *testing.T) {
	store, org := zeroStore(t, "zg")
	fund(t, store, org, 1)
	drain(t, store, org)
	zero := NewCreditZeroStore(store)
	ctx := context.Background()
	if err := zero.MarkZero(ctx, org); err != nil {
		t.Fatal(err)
	}
	first := orgState(t, zero, org)
	if first.Since == nil || first.OwnerSuspended {
		t.Fatalf("%+v", first)
	}
	if _, err := store.db.Exec(`UPDATE credit_zero_orgs SET since = since - interval '3 days' WHERE org_id = $1`, org); err != nil {
		t.Fatal(err)
	}
	if err := zero.MarkZero(ctx, org); err != nil {
		t.Fatal(err)
	}
	if again := orgState(t, zero, org); !again.Since.Before(*first.Since) {
		t.Fatal("marking again keeps the first date")
	}
	if _, err := store.db.Exec(`INSERT INTO abuse_suspensions (user_id, email, reason, reason_code, suspended_at) VALUES ('zg', 'zg@x.test', 'r', 'other', now())`); err != nil {
		t.Fatal(err)
	}
	if !orgState(t, zero, org).OwnerSuspended {
		t.Fatal("the owner's suspension is visible")
	}
	if err := zero.ClearZero(ctx, org); err != nil {
		t.Fatal(err)
	}
	if orgState(t, zero, org).Since != nil {
		t.Fatal("cleared")
	}
}

func orgState(t *testing.T, zero *CreditZeroStore, org string) creditzero.OrgState {
	t.Helper()
	orgs, err := zero.Orgs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range orgs {
		if o.OrgID == org {
			return o
		}
	}
	t.Fatalf("org %s not listed in %v", org, orgs)
	return creditzero.OrgState{}
}

// The credit refusals and R12, through the real store and the real gate.
func TestAtZeroGrowthIsRefusedAtOnceAndNamesTheCredit(t *testing.T) {
	store, org := zeroStore(t, "zr")
	fund(t, store, org, 5)
	newDatabase(t, store, "proj-zr-a", org)
	newDatabase(t, store, "proj-zr-b", org)
	drain(t, store, org)

	err := store.Create(instanceRow("proj-zr-c", org))
	if !isCreditRefusal(err) {
		t.Fatalf("a create at $0 that passes the 1000m allowance: %v", err)
	}
	if got, _ := store.FindByProjectID("proj-zr-c"); got != nil {
		t.Fatal("a refused create leaves no row")
	}
	_, err = store.ReserveDatabaseSize(context.Background(), "proj-zr-a", planbudget.Reservation{PerCopy: planbudget.Size{CPUMilli: 600, MemoryBytes: 512 << 20, DiskBytes: 5 << 30}, Copies: 1})
	if !isCreditRefusal(err) {
		t.Fatalf("growth at $0: %v", err)
	}

	fund(t, store, org, 5)
	if err := store.Create(instanceRow("proj-zr-c", org)); err != nil {
		t.Fatalf("with credit the same create fits the plan: %v", err)
	}
}

func TestAtZeroTheDiskIsFrozen(t *testing.T) {
	store, org := zeroStore(t, "zd")
	fund(t, store, org, 5)
	newDatabase(t, store, "proj-zd-a", org)
	drain(t, store, org)
	if err := store.UpdateStorageSizeIfStatus("proj-zd-a", "6Gi", "ACTIVE"); !isCreditRefusal(err) {
		t.Fatalf("a bigger disk at $0, though 6 GiB is far under the 20 GiB allowance: %v", err)
	}
	fund(t, store, org, 5)
	if err := store.UpdateStorageSizeIfStatus("proj-zd-a", "6Gi", "ACTIVE"); err != nil {
		t.Fatalf("with credit it grows: %v", err)
	}
}

func TestAtZeroAndOverTheAllowanceShrinkPauseResizeDownAndDeleteStillPass(t *testing.T) {
	store, org := zeroStore(t, "zo")
	fund(t, store, org, 5)
	for _, project := range []string{"proj-zo-a", "proj-zo-b", "proj-zo-c", "proj-zo-d"} {
		newDatabase(t, store, project, org) // 4 x 500m = 2000m against a 1000m allowance
	}
	drain(t, store, org)
	ctx := context.Background()

	if _, err := store.ReserveDatabaseSize(ctx, "proj-zo-a", planbudget.Reservation{PerCopy: planbudget.Size{CPUMilli: 250, MemoryBytes: 256 << 20, DiskBytes: 5 << 30}, Copies: 1}); err != nil {
		t.Errorf("resize down was refused: %v", err)
	}
	paused, _ := store.FindByProjectID("proj-zo-b")
	paused.Status = string(domain.StatusPaused)
	if err := store.UpdateIfStatus(paused, "ACTIVE"); err != nil {
		t.Errorf("pause was refused: %v", err)
	}
	if err := store.Delete("proj-zo-c"); err != nil {
		t.Errorf("delete was refused: %v", err)
	}
	resumed, _ := store.FindByProjectID("proj-zo-b")
	resumed.Status = "ACTIVE"
	if err := store.UpdateIfStatus(resumed, string(domain.StatusPaused)); !isCreditRefusal(err) {
		t.Errorf("resuming a paused database back over the allowance is growth: %v", err)
	}
}

func TestHoldingsAreWhatTheBudgetSumsAndListOnlyServicesThatCanBePaused(t *testing.T) {
	store, org := zeroStore(t, "zh")
	fund(t, store, org, 5)
	newDatabase(t, store, "proj-zh-a", org)
	newDatabase(t, store, "proj-zh-idle", org)
	newDatabase(t, store, "proj-zh-new", org)
	ageDatabase(t, store, "proj-zh-a", 10*24*time.Hour)
	ageDatabase(t, store, "proj-zh-new", time.Hour)
	idle := mustFind(t, store, "proj-zh-idle")
	idle.Status, idle.PauseReason = string(domain.StatusPaused), domain.PauseReasonIdle
	if err := store.UpdateIfStatus(idle, "ACTIVE"); err != nil {
		t.Fatal(err)
	}
	addApp(t, store, "proj-zh-a", "app-run", "ACTIVE", 2, 5*24*time.Hour)
	addApp(t, store, "proj-zh-new", "app-stop", "PAUSED", 1, 4*24*time.Hour)
	addApp(t, store, "proj-zh-new", "app-roll", "RESUMING", 1, 3*24*time.Hour)
	addApp(t, store, "proj-zh-new", "app-fail", "FAILED", 1, 2*24*time.Hour)
	addApp(t, store, "proj-zh-new", "app-zero", "ACTIVE", 0, 2*24*time.Hour)

	held, err := NewCreditZeroStore(store).Holdings(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}
	used, err := planbudget.OrgUsage(context.Background(), store.db, org)
	if err != nil {
		t.Fatal(err)
	}
	if held.CPUMilli != used.CPUMilli || held.MemoryBytes != used.MemoryBytes {
		t.Fatalf("the totals are the budget's own sum: %+v vs %+v", held, used)
	}
	var ids []string
	for _, s := range held.Pausable {
		ids = append(ids, s.ServiceID)
	}
	slices.Sort(ids)
	want := []string{"app-run", "proj-zh-a", "proj-zh-new"}
	if !slices.Equal(ids, want) {
		t.Fatalf("running databases and running apps with replicas only; a paused database, a stopped app, a failed one, one with no replicas and one still starting are not candidates: %v", ids)
	}
	for _, s := range held.Pausable {
		if s.ServiceID == "app-run" && (s.CPUMilli <= 0 || s.MemoryBytes <= 0 || s.Kind != creditzero.KindApp || s.ProjectID != "proj-zh-a") {
			t.Fatalf("an app carries its size: %+v", s)
		}
		if s.ServiceID == "proj-zh-a" && (s.Kind != creditzero.KindDatabase || s.CPUMilli != 500) {
			t.Fatalf("a database carries its size: %+v", s)
		}
	}
	oldest := heldByID(held, "proj-zh-a").CreatedAt
	if !oldest.Before(heldByID(held, "proj-zh-new").CreatedAt) {
		t.Fatal("creation times are read, so newest-first is real")
	}
}

func heldByID(h creditzero.Holdings, id string) creditzero.Service {
	for _, s := range h.Pausable {
		if s.ServiceID == id {
			return s
		}
	}
	return creditzero.Service{}
}

func mustFind(t *testing.T, store *Store, project string) *domain.DatabaseInstance {
	t.Helper()
	inst, err := store.FindByProjectID(project)
	if err != nil || inst == nil {
		t.Fatalf("find %s: %v", project, err)
	}
	return inst
}

func TestPausedRowsReadTheLiveStateOfEachService(t *testing.T) {
	store, org := zeroStore(t, "zp2")
	fund(t, store, org, 5)
	zero := NewCreditZeroStore(store)
	ctx := context.Background()
	set := func(project, status, reason string) {
		inst := mustFind(t, store, project)
		inst.Status, inst.PauseReason = status, reason
		if _, err := store.db.Exec(`UPDATE database_instances SET status = $2, pause_reason = $3 WHERE project_id = $1`, project, status, reason); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"d-credit", "d-pausing", "d-userpausing", "d-provisioning", "d-idle", "d-active", "d-resuming", "d-deleting"} {
		newDatabase(t, store, "proj-"+p, org)
	}
	set("proj-d-credit", "PAUSED", domain.PauseReasonCredits)
	set("proj-d-pausing", "PAUSING", domain.PauseReasonCredits)
	set("proj-d-userpausing", "PAUSING", domain.PauseReasonIdle)
	set("proj-d-provisioning", "PROVISIONING", "")
	set("proj-d-idle", "PAUSED", domain.PauseReasonIdle)
	set("proj-d-resuming", "RESUMING", domain.PauseReasonCredits)
	set("proj-d-deleting", "DELETING", "")
	addApp(t, store, "proj-d-active", "a-stopped", "PAUSED", 1, time.Hour)
	addApp(t, store, "proj-d-active", "a-running", "ACTIVE", 1, time.Hour)
	addApp(t, store, "proj-d-active", "a-resuming", "RESUMING", 1, time.Hour)
	addApp(t, store, "proj-d-active", "a-pausing", "PAUSING", 1, time.Hour)

	rows := map[string]creditzero.Hold{
		"database/proj-d-credit/proj-d-credit":             creditzero.HoldPaused,
		"database/proj-d-pausing/proj-d-pausing":           creditzero.HoldBusy,
		"database/proj-d-userpausing/proj-d-userpausing":   creditzero.HoldReleased,
		"database/proj-d-provisioning/proj-d-provisioning": creditzero.HoldBusy,
		"database/proj-d-idle/proj-d-idle":                 creditzero.HoldReleased,
		"database/proj-d-active/proj-d-active":             creditzero.HoldReleased,
		"database/proj-d-resuming/proj-d-resuming":         creditzero.HoldBusy,
		"database/proj-d-deleting/proj-d-deleting":         creditzero.HoldGone,
		"database/proj-nowhere/proj-nowhere":               creditzero.HoldGone,
		"app/proj-d-active/a-stopped":                      creditzero.HoldPaused,
		"app/proj-d-active/a-pausing":                      creditzero.HoldBusy,
		"app/proj-d-active/a-running":                      creditzero.HoldReleased,
		"app/proj-d-active/a-resuming":                     creditzero.HoldBusy,
		"app/proj-d-active/a-missing":                      creditzero.HoldGone,
	}
	for key := range rows {
		kind, project, service := splitKey(key)
		if err := zero.RecordPause(ctx, org, creditzero.Paused{Kind: kind, ProjectID: project, ServiceID: service, Name: service}); err != nil {
			t.Fatalf("record %s: %v", key, err)
		}
	}
	got, err := zero.Paused(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(rows) {
		t.Fatalf("%d rows, want %d", len(got), len(rows))
	}
	for _, row := range got {
		if want := rows[row.Key()]; row.State != want {
			t.Errorf("%s is %d, want %d", row.Key(), row.State, want)
		}
	}
}

func splitKey(key string) (kind, project, service string) {
	parts := strings.SplitN(key, "/", 3)
	return parts[0], parts[1], parts[2]
}

func TestRecordingTheSamePauseTwiceKeepsOneRowAndForgetRemovesIt(t *testing.T) {
	store, org := zeroStore(t, "zk")
	zero := NewCreditZeroStore(store)
	ctx := context.Background()
	row := creditzero.Paused{Kind: creditzero.KindApp, ProjectID: "proj-x", ServiceID: "a1", Name: "web"}
	for range 2 {
		if err := zero.RecordPause(ctx, org, row); err != nil {
			t.Fatal(err)
		}
	}
	rows, _ := zero.Paused(ctx, org)
	if len(rows) != 1 || rows[0].Name != "web" {
		t.Fatalf("%+v", rows)
	}
	if err := zero.ForgetPause(ctx, org, row); err != nil {
		t.Fatal(err)
	}
	if rows, _ := zero.Paused(ctx, org); len(rows) != 0 {
		t.Fatalf("%+v", rows)
	}
}

func TestPurgeOrphansDropsRowsOfOrgsThatNoLongerExist(t *testing.T) {
	store, org := zeroStore(t, "zq")
	zero := NewCreditZeroStore(store)
	ctx := context.Background()
	for _, id := range []string{org, "org-deleted"} {
		if err := zero.RecordPause(ctx, id, creditzero.Paused{Kind: creditzero.KindApp, ProjectID: "p", ServiceID: "a-" + id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := zero.PurgeOrphans(ctx); err != nil {
		t.Fatal(err)
	}
	var left, live int
	if err := store.db.QueryRow(`SELECT count(*), count(*) FILTER (WHERE org_id = $1) FROM credit_paused_services`, org).Scan(&left, &live); err != nil || left != 1 || live != 1 {
		t.Fatalf("only the live org's row stays: %d of %d %v", live, left, err)
	}
}

func TestHeldForCreditIsTrueOnlyForARecordedAppOfAnOrgAtZero(t *testing.T) {
	store, org := zeroStore(t, "zhc")
	fund(t, store, org, 5)
	newDatabase(t, store, "proj-zhc", org)
	zero := NewCreditZeroStore(store)
	ctx := context.Background()
	if err := zero.RecordPause(ctx, org, creditzero.Paused{Kind: creditzero.KindApp, ProjectID: "proj-zhc", ServiceID: "a1"}); err != nil {
		t.Fatal(err)
	}
	held := func(project, app string) bool {
		ok, err := zero.HeldForCredit(ctx, project, app)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if held("proj-zhc", "a1") {
		t.Fatal("an org with credit holds nothing for credit")
	}
	drain(t, store, org)
	if !held("proj-zhc", "a1") || held("proj-zhc", "other-app") || held("proj-else", "a1") {
		t.Fatal("at $0 the recorded app is held, and only that app")
	}
	fund(t, store, org, 1)
	if held("proj-zhc", "a1") {
		t.Fatal("credit back, nothing held")
	}
}

// pauseApplying pauses and resumes for real in the platform database, as the lifecycle services do.
type pauseApplying struct {
	store *Store
	calls []string
}

func (p *pauseApplying) Pause(_ context.Context, row creditzero.Paused, admitted func() error) error {
	if err := admitted(); err != nil {
		return err
	}
	p.calls = append(p.calls, "pause "+row.ServiceID)
	if row.Kind == creditzero.KindDatabase {
		_, err := p.store.db.Exec(`UPDATE database_instances SET status = 'PAUSED', pause_reason = $2 WHERE project_id = $1`, row.ProjectID, domain.PauseReasonCredits)
		return err
	}
	_, err := p.store.db.Exec(`UPDATE apps SET status = 'PAUSED', doc = jsonb_set(doc, '{status}', '"PAUSED"') WHERE project_id = $1 AND id = $2`, row.ProjectID, row.ServiceID)
	return err
}

func (p *pauseApplying) Resume(_ context.Context, row creditzero.Paused) error {
	p.calls = append(p.calls, "resume "+row.ServiceID)
	if row.Kind == creditzero.KindDatabase {
		_, err := p.store.db.Exec(`UPDATE database_instances SET status = 'ACTIVE', pause_reason = '' WHERE project_id = $1`, row.ProjectID)
		return err
	}
	_, err := p.store.db.Exec(`UPDATE apps SET status = 'ACTIVE', doc = jsonb_set(doc, '{status}', '"ACTIVE"') WHERE project_id = $1 AND id = $2`, row.ProjectID, row.ServiceID)
	return err
}

func zeroEnforcer(store *Store, work creditzero.Workloads, gate *planbudget.Gate) *creditzero.Enforcer {
	allowance := func(tier string) (int64, int64, bool) {
		size, ok := gate.Allowance(tier)
		return size.CPUMilli, size.MemoryBytes, ok
	}
	return creditzero.New(NewCreditZeroStore(store), work, allowance, 7*24*time.Hour)
}

// The whole path through the real store: out of credit, a week of grace, the newest compute pauses until it
// fits the allowance and nothing else, then credit comes back and exactly those resume.
func TestEndToEndTheGracePausesTheNewestAndCreditResumesExactlyThose(t *testing.T) {
	store, org := zeroStore(t, "ze")
	fund(t, store, org, 5)
	// a free app is 250m / 256 MiB per replica; the database is 500m / 512 MiB.
	newDatabase(t, store, "proj-ze", org)
	ageDatabase(t, store, "proj-ze", 30*24*time.Hour)
	addApp(t, store, "proj-ze", "app-old", "ACTIVE", 1, 20*24*time.Hour)
	addApp(t, store, "proj-ze", "app-mid", "ACTIVE", 1, 10*24*time.Hour)
	addApp(t, store, "proj-ze", "app-new", "ACTIVE", 2, 1*24*time.Hour)
	drain(t, store, org)

	work := &pauseApplying{store: store}
	enforcer := zeroEnforcer(store, work, zeroGate())
	ctx := context.Background()
	used, err := planbudget.OrgUsage(ctx, store.db, org)
	if err != nil {
		t.Fatal(err)
	}
	if used.CPUMilli <= 1000 {
		t.Fatalf("the scenario must start over the 1000m allowance, holds %dm", used.CPUMilli)
	}

	if err := enforcer.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if len(work.calls) != 0 {
		t.Fatalf("a first pass only starts the grace period: %v", work.calls)
	}
	if _, err := store.db.Exec(`UPDATE credit_zero_orgs SET since = now() - interval '7 days 1 minute' WHERE org_id = $1`, org); err != nil {
		t.Fatal(err)
	}
	if err := enforcer.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	rows, _ := NewCreditZeroStore(store).Paused(ctx, org)
	if len(rows) == 0 {
		t.Fatal("compute above the allowance was paused")
	}
	if slices.Contains(work.calls, "pause proj-ze") {
		t.Fatalf("the oldest service, the database, is the last to go: %v", work.calls)
	}
	after, err := planbudget.OrgUsage(ctx, store.db, org)
	if err != nil {
		t.Fatal(err)
	}
	if after.CPUMilli > 1000 || after.MemoryBytes > 1024<<20 {
		t.Fatalf("what is left fits the allowance: %dm %d MiB", after.CPUMilli, after.MemoryBytes>>20)
	}
	if after.DiskBytes != used.DiskBytes {
		t.Fatal("disk is never touched")
	}
	paused := len(rows)
	if err := enforcer.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if rows, _ := NewCreditZeroStore(store).Paused(ctx, org); len(rows) != paused {
		t.Fatalf("a second pass finds nothing more to pause: %d then %d", paused, len(rows))
	}

	fund(t, store, org, 5)
	work.calls = nil
	if err := enforcer.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if rows, _ := NewCreditZeroStore(store).Paused(ctx, org); len(rows) != 0 {
		t.Fatalf("every row is released: %+v", rows)
	}
	if len(work.calls) != paused {
		t.Fatalf("exactly the paused services resumed: %v", work.calls)
	}
	back, _ := planbudget.OrgUsage(ctx, store.db, org)
	if back.CPUMilli != used.CPUMilli {
		t.Fatalf("everything runs again: %dm vs %dm", back.CPUMilli, used.CPUMilli)
	}
	var marks int
	if err := store.db.QueryRow(`SELECT count(*) FROM credit_zero_orgs WHERE org_id = $1`, org).Scan(&marks); err != nil || marks != 0 {
		t.Fatalf("the grace period ended with the credit: %d %v", marks, err)
	}
}

func TestEndToEndAnIdlePausedDatabaseIsNeverTouchedByCredit(t *testing.T) {
	store, org := zeroStore(t, "zi")
	fund(t, store, org, 5)
	newDatabase(t, store, "proj-zi", org)
	idle := mustFind(t, store, "proj-zi")
	idle.Status, idle.PauseReason = string(domain.StatusPaused), domain.PauseReasonIdle
	if err := store.UpdateIfStatus(idle, "ACTIVE"); err != nil {
		t.Fatal(err)
	}
	addApp(t, store, "proj-zi", "app-a", "ACTIVE", 4, time.Hour)
	drain(t, store, org)
	work := &pauseApplying{store: store}
	enforcer := zeroEnforcer(store, work, zeroGate())
	ctx := context.Background()
	if err := enforcer.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE credit_zero_orgs SET since = now() - interval '8 days'`); err != nil {
		t.Fatal(err)
	}
	if err := enforcer.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(work.calls, "pause proj-zi") {
		t.Fatal("a database the user's idle pause already stopped is not credit's to pause")
	}
	fund(t, store, org, 5)
	work.calls = nil
	if err := enforcer.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(work.calls, "resume proj-zi") {
		t.Fatal("and credit never resumes it")
	}
	if got := mustFind(t, store, "proj-zi"); got.PauseReason != domain.PauseReasonIdle || got.Status != string(domain.StatusPaused) {
		t.Fatalf("still idle-paused: %s %s", got.Status, got.PauseReason)
	}
}

func TestEndToEndTurningMeteringOffReleasesEveryServiceAndEveryGrace(t *testing.T) {
	store, org := zeroStore(t, "zo2")
	fund(t, store, org, 5)
	newDatabase(t, store, "proj-zo2", org)
	addApp(t, store, "proj-zo2", "app-a", "ACTIVE", 4, time.Hour)
	drain(t, store, org)
	work := &pauseApplying{store: store}
	enforcer := zeroEnforcer(store, work, zeroGate())
	ctx := context.Background()
	_ = enforcer.Pass(ctx)
	if _, err := store.db.Exec(`UPDATE credit_zero_orgs SET since = now() - interval '8 days'`); err != nil {
		t.Fatal(err)
	}
	if err := enforcer.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if rows, _ := NewCreditZeroStore(store).Paused(ctx, org); len(rows) == 0 {
		t.Fatal("the scenario must pause something first")
	}
	if err := enforcer.ReleaseAll(ctx); err != nil {
		t.Fatal(err)
	}
	var rows, marks int
	if err := store.db.QueryRow(`SELECT (SELECT count(*) FROM credit_paused_services), (SELECT count(*) FROM credit_zero_orgs)`).Scan(&rows, &marks); err != nil || rows != 0 || marks != 0 {
		t.Fatalf("rows %d, marks %d, %v", rows, marks, err)
	}
	if got := mustFind(t, store, "proj-zo2"); got.Status != "ACTIVE" {
		t.Fatalf("the database runs again: %s", got.Status)
	}
}

func TestOrgZeroReportsTheStateStudioShows(t *testing.T) {
	store, org := zeroStore(t, "zv")
	fund(t, store, org, 5)
	newDatabase(t, store, "proj-zv", org)
	addApp(t, store, "proj-zv", "app-web", "PAUSED", 1, time.Hour)
	addApp(t, store, "proj-zv", "app-user", "ACTIVE", 1, time.Hour)
	zero := NewCreditZeroStore(store)
	ctx := context.Background()
	for _, id := range []string{"app-web", "app-user"} {
		if err := zero.RecordPause(ctx, org, creditzero.Paused{Kind: creditzero.KindApp, ProjectID: "proj-zv", ServiceID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	info, err := zero.OrgZero(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if info.AtZero || info.Since != nil {
		t.Fatalf("with credit: %+v", info)
	}
	if len(info.Paused) != 1 || info.Paused[0].ServiceID != "app-web" || info.Paused[0].Name != "app-web" || info.Paused[0].PausedAt.IsZero() {
		t.Fatalf("only a service that is really paused for credit is listed, a resumed one is not: %+v", info.Paused)
	}
	drain(t, store, org)
	if err := zero.MarkZero(ctx, org); err != nil {
		t.Fatal(err)
	}
	info, err = zero.OrgZero(ctx, org)
	if err != nil || !info.AtZero || info.Since == nil {
		t.Fatalf("at zero with a grace start: %+v %v", info, err)
	}
}

func TestCreditAnalyticsCountsOrgsAtZeroAndWhatCreditPaused(t *testing.T) {
	store, spent := zeroStore(t, "ca1")
	fund(t, store, spent, 5)
	drain(t, store, spent)
	other := creditPerson(t, store, "ca2")
	fund(t, store, other, 5)
	drain(t, store, other)
	funded := creditPerson(t, store, "ca3")
	fund(t, store, funded, 5)
	paid := orgOf(t, store, "ca4")
	if _, err := store.db.Exec(`UPDATE orgs SET tier = 'STANDARD' WHERE id = $1`, paid); err != nil {
		t.Fatal(err)
	}
	fund(t, store, paid, 5)
	drain(t, store, paid)
	zero := NewCreditZeroStore(store)
	ctx := context.Background()
	for _, id := range []string{"a1", "a2"} {
		if err := zero.RecordPause(ctx, spent, creditzero.Paused{Kind: creditzero.KindApp, ProjectID: "p", ServiceID: id}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.AdminConsole().CreditAnalytics(ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	if got.OrgsAtZero != 2 || got.OrgsWithCredit != 1 || got.OrgsWithPausedServices != 1 || got.ServicesPaused != 2 {
		t.Fatalf("two FREE orgs are at zero, one has credit, a paid org is neither: %+v", got)
	}
}

func TestADatabaseWithNoCreationTimeCountsAsTheOldest(t *testing.T) {
	store, org := zeroStore(t, "zo")
	fund(t, store, org, 5)
	newDatabase(t, store, "proj-zo-undated", org)
	newDatabase(t, store, "proj-zo-dated", org)
	ageDatabase(t, store, "proj-zo-dated", 400*24*time.Hour)
	if _, err := store.db.Exec(`UPDATE database_instances SET created_at = NULL WHERE project_id = 'proj-zo-undated'`); err != nil {
		t.Fatal(err)
	}
	held, err := NewCreditZeroStore(store).Holdings(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}
	if !heldByID(held, "proj-zo-undated").CreatedAt.Before(heldByID(held, "proj-zo-dated").CreatedAt) {
		t.Fatal("what has no date is the last to be paused, not the first")
	}
}

func TestHoldingsReportAnOrgWithAServiceInTransition(t *testing.T) {
	for name, seed := range map[string]func(t *testing.T, store *Store, org string){
		"a database pausing": func(t *testing.T, store *Store, org string) {
			setDatabaseStatus(t, store, "proj-zt-d", "PAUSING")
		},
		"a database resuming": func(t *testing.T, store *Store, org string) {
			setDatabaseStatus(t, store, "proj-zt-d", "RESUMING")
		},
		"a database provisioning": func(t *testing.T, store *Store, org string) {
			setDatabaseStatus(t, store, "proj-zt-d", "PROVISIONING")
		},
		"an app pausing": func(t *testing.T, store *Store, org string) {
			addApp(t, store, "proj-zt-d", "a1", "PAUSING", 1, time.Hour)
		},
		"an app resuming": func(t *testing.T, store *Store, org string) {
			addApp(t, store, "proj-zt-d", "a1", "RESUMING", 1, time.Hour)
		},
		"an app still being created": func(t *testing.T, store *Store, org string) {
			addApp(t, store, "proj-zt-d", "a1", "PROVISIONING", 1, time.Hour)
		},
	} {
		t.Run(name, func(t *testing.T) {
			store, org := zeroStore(t, "zt"+strings.ReplaceAll(name, " ", ""))
			fund(t, store, org, 5)
			newDatabase(t, store, "proj-zt-d", org)
			zero := NewCreditZeroStore(store)
			settled, err := zero.Holdings(context.Background(), org)
			if err != nil || settled.Transitioning {
				t.Fatalf("a settled org is not in transition: %v %+v", err, settled)
			}
			seed(t, store, org)
			moving, err := zero.Holdings(context.Background(), org)
			if err != nil || !moving.Transitioning {
				t.Fatalf("expected a transition: %v %+v", err, moving)
			}
		})
	}
}

func setDatabaseStatus(t *testing.T, store *Store, project, status string) {
	t.Helper()
	if _, err := store.db.Exec(`UPDATE database_instances SET status = $2 WHERE project_id = $1`, project, status); err != nil {
		t.Fatal(err)
	}
}

func TestOrgReadsOneOrgAgainAndAnOrgWithNoAccountIsNeverAtZero(t *testing.T) {
	store, spent := zeroStore(t, "zr")
	fund(t, store, spent, 5)
	drain(t, store, spent)
	bare := creditPerson(t, store, "zr-bare")
	zero := NewCreditZeroStore(store)
	ctx := context.Background()
	got, err := zero.Org(ctx, spent)
	if err != nil || got.OrgID != spent || !got.AtZero || got.Tier != "FREE" {
		t.Fatalf("%v %+v", err, got)
	}
	fund(t, store, spent, 5)
	if again, _ := zero.Org(ctx, spent); again.AtZero {
		t.Fatal("it reads the state now, not a cached one")
	}
	other := creditPerson(t, store, "zr-other")
	fund(t, store, other, 5)
	for _, id := range []string{spent, other, bare} {
		if one, err := zero.Org(ctx, id); err != nil || one.OrgID != id {
			t.Fatalf("asked for %s, got %+v %v", id, one, err)
		}
	}
	none, err := zero.Org(ctx, bare)
	if err != nil || none.AtZero {
		t.Fatalf("%v %+v", err, none)
	}
}
