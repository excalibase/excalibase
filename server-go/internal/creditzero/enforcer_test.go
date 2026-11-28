package creditzero

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

var epoch = time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)

const (
	mib   = int64(1) << 20
	grace = 7 * 24 * time.Hour
)

// fakeStore is an in-memory store with the behaviour of the real one: the rows it is
// asked to keep are the rows it lists.
type fakeStore struct {
	now      time.Time
	orgs     []OrgState
	holdings map[string]Holdings
	paused   map[string][]Paused
	zero     map[string]time.Time
	events   *[]string
}

func newFakeStore(events *[]string, orgs ...OrgState) *fakeStore {
	return &fakeStore{now: epoch, orgs: orgs, holdings: map[string]Holdings{}, paused: map[string][]Paused{}, zero: map[string]time.Time{}, events: events}
}

func (f *fakeStore) log(format string, args ...any) {
	*f.events = append(*f.events, fmt.Sprintf(format, args...))
}

func (f *fakeStore) Now(context.Context) (time.Time, error) { return f.now, nil }

func (f *fakeStore) Orgs(context.Context) ([]OrgState, error) {
	out := slices.Clone(f.orgs)
	for i := range out {
		if since, ok := f.zero[out[i].OrgID]; ok {
			out[i].Since = &since
		}
	}
	return out, nil
}

func (f *fakeStore) MarkZero(_ context.Context, orgID string) error {
	if _, ok := f.zero[orgID]; !ok {
		f.zero[orgID] = f.now
		f.log("mark-zero %s", orgID)
	}
	return nil
}

func (f *fakeStore) ClearZero(_ context.Context, orgID string) error {
	if _, ok := f.zero[orgID]; ok {
		delete(f.zero, orgID)
		f.log("clear-zero %s", orgID)
	}
	return nil
}

func (f *fakeStore) Holdings(_ context.Context, orgID string) (Holdings, error) {
	return f.holdings[orgID], nil
}

func (f *fakeStore) RecordPause(_ context.Context, orgID string, row Paused) error {
	f.paused[orgID] = append(f.paused[orgID], row)
	f.log("record %s", row.ServiceID)
	return nil
}

func (f *fakeStore) ForgetPause(_ context.Context, orgID string, row Paused) error {
	f.paused[orgID] = slices.DeleteFunc(f.paused[orgID], func(p Paused) bool { return p.Key() == row.Key() })
	f.log("forget %s", row.ServiceID)
	return nil
}

func (f *fakeStore) Paused(_ context.Context, orgID string) ([]Paused, error) {
	return slices.Clone(f.paused[orgID]), nil
}

func (f *fakeStore) PurgeOrphans(context.Context) error { return nil }

func (f *fakeStore) pausedIDs(orgID string) []string {
	var ids []string
	for _, row := range f.paused[orgID] {
		ids = append(ids, row.ServiceID)
	}
	slices.Sort(ids)
	return ids
}

type fakeWorkloads struct {
	events    *[]string
	pauseErr  map[string]error
	resumeErr map[string]error
}

func (w *fakeWorkloads) Pause(_ context.Context, row Paused) error {
	*w.events = append(*w.events, "pause "+row.ServiceID)
	return w.pauseErr[row.ServiceID]
}

func (w *fakeWorkloads) Resume(_ context.Context, row Paused) error {
	*w.events = append(*w.events, "resume "+row.ServiceID)
	return w.resumeErr[row.ServiceID]
}

func freeAllowance(tier string) (int64, int64, bool) {
	if tier == "FREE" {
		return 1000, 1024 * mib, true
	}
	return 0, 0, false
}

type rig struct {
	store    *fakeStore
	work     *fakeWorkloads
	enforcer *Enforcer
	events   *[]string
}

func newRig(orgs ...OrgState) *rig {
	events := &[]string{}
	store := newFakeStore(events, orgs...)
	work := &fakeWorkloads{events: events}
	return &rig{store: store, work: work, enforcer: New(store, work, freeAllowance, grace), events: events}
}

func zeroOrg(since *time.Time) OrgState {
	return OrgState{OrgID: "org-1", Tier: "FREE", AtZero: true, Since: since}
}

func service(kind, id string, cpu, memMiB int64, age time.Duration) Service {
	return Service{Kind: kind, ProjectID: "proj-" + id, ServiceID: id, Name: id, CPUMilli: cpu, MemoryBytes: memMiB * mib, CreatedAt: epoch.Add(-age), Replicas: 1}
}

func holding(cpu, memMiB int64, pausable ...Service) Holdings {
	return Holdings{CPUMilli: cpu, MemoryBytes: memMiB * mib, Pausable: pausable}
}

func at(offset time.Duration) *time.Time {
	t := epoch.Add(offset)
	return &t
}

func TestAnOrgThatJustRanOutIsMarkedAndNothingPauses(t *testing.T) {
	r := newRig(zeroOrg(nil))
	r.store.holdings["org-1"] = holding(2000, 2048, service(KindApp, "a1", 2000, 2048, time.Hour))
	if err := r.enforcer.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, marked := r.store.zero["org-1"]; !marked {
		t.Fatal("the grace period starts when the credit ran out")
	}
	if strings.Contains(strings.Join(*r.events, ","), "pause") {
		t.Fatalf("nothing pauses on the first day: %v", *r.events)
	}
}

func TestDuringTheGraceNothingPausesAndAfterItComputeAboveTheAllowanceDoes(t *testing.T) {
	for name, tc := range map[string]struct {
		since time.Duration
		want  bool
	}{
		"a second before the end": {-grace + time.Second, false},
		"exactly at the end":      {-grace, true},
		"long after":              {-3 * grace, true},
	} {
		r := newRig(zeroOrg(at(tc.since)))
		r.store.holdings["org-1"] = holding(2000, 2048, service(KindApp, "a1", 2000, 2048, time.Hour))
		if err := r.enforcer.Pass(context.Background()); err != nil {
			t.Fatal(err)
		}
		if paused := len(r.store.paused["org-1"]) == 1; paused != tc.want {
			t.Errorf("%s: paused=%v, want %v (%v)", name, paused, tc.want, *r.events)
		}
	}
}

func TestPausingGoesNewestFirstAndStopsOnceComputeFits(t *testing.T) {
	r := newRig(zeroOrg(at(-grace)))
	// 1750m / 1792 MiB held against 1000m / 1024 MiB.
	r.store.holdings["org-1"] = holding(1750, 1792,
		service(KindDatabase, "db", 500, 512, 10*24*time.Hour),
		service(KindApp, "old-app", 500, 512, 5*24*time.Hour),
		service(KindApp, "new-app", 500, 512, 2*24*time.Hour),
		service(KindApp, "newest", 250, 256, time.Hour))
	if err := r.enforcer.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.store.pausedIDs("org-1"); !slices.Equal(got, []string{"new-app", "newest"}) {
		t.Fatalf("the newest two bring 1000m/1024MiB under the allowance: %v", got)
	}
}

func TestEitherCPUOrMemoryOverTheAllowanceKeepsPausing(t *testing.T) {
	r := newRig(zeroOrg(at(-grace)))
	r.store.holdings["org-1"] = holding(800, 1700,
		service(KindApp, "heavy-old", 300, 900, 3*time.Hour),
		service(KindApp, "light-new", 500, 600, time.Hour))
	if err := r.enforcer.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.store.pausedIDs("org-1"); !slices.Equal(got, []string{"heavy-old", "light-new"}) {
		t.Fatalf("CPU fits at once but memory needs both paused: %v", got)
	}
}

func TestAnOrgWithinTheAllowanceIsLeftAloneAfterTheGrace(t *testing.T) {
	r := newRig(zeroOrg(at(-2 * grace)))
	r.store.holdings["org-1"] = holding(1000, 1024, service(KindApp, "a1", 1000, 1024, time.Hour))
	if err := r.enforcer.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.store.paused["org-1"]) != 0 || strings.Contains(strings.Join(*r.events, ","), "pause") {
		t.Fatalf("exactly the allowance fits: %v", *r.events)
	}
}

func TestThePauseIsRecordedBeforeItRunsSoACrashCannotLoseTheService(t *testing.T) {
	r := newRig(zeroOrg(at(-grace)))
	r.store.holdings["org-1"] = holding(2000, 2048, service(KindApp, "a1", 2000, 2048, time.Hour))
	if err := r.enforcer.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(*r.events, ","); got != "record a1,pause a1" {
		t.Fatalf("record first, then pause: %s", got)
	}
}

func TestAPauseThatFailsLeavesNoRowAndTheRestStillPause(t *testing.T) {
	r := newRig(zeroOrg(at(-grace)))
	r.store.holdings["org-1"] = holding(1500, 1536,
		service(KindApp, "old", 500, 512, 2*time.Hour),
		service(KindApp, "new", 1000, 1024, time.Hour))
	r.work.pauseErr = map[string]error{"new": errors.New("pods remain")}
	err := r.enforcer.Pass(context.Background())
	if err == nil || !strings.Contains(err.Error(), "pods remain") {
		t.Fatalf("the failure is reported, not swallowed: %v", err)
	}
	if got := r.store.pausedIDs("org-1"); !slices.Equal(got, []string{"old"}) {
		t.Fatalf("the failed one has no row and the next newest was tried: %v", got)
	}
}

func TestPausedAppRecordsTheReplicasItRan(t *testing.T) {
	r := newRig(zeroOrg(at(-grace)))
	big := service(KindApp, "a1", 2000, 2048, time.Hour)
	big.Replicas = 3
	r.store.holdings["org-1"] = holding(2000, 2048, big)
	if err := r.enforcer.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.store.paused["org-1"][0].PreviousReplicas; got != 3 {
		t.Fatalf("replicas %d", got)
	}
}

func TestCreditComingBackClearsTheGraceAndResumesExactlyTheRecordedServices(t *testing.T) {
	r := newRig(OrgState{OrgID: "org-1", Tier: "FREE", AtZero: false})
	r.store.zero["org-1"] = epoch.Add(-time.Hour)
	r.store.paused["org-1"] = []Paused{
		{Kind: KindApp, ProjectID: "p1", ServiceID: "a1", State: HoldPaused},
		{Kind: KindDatabase, ProjectID: "p2", ServiceID: "p2", State: HoldPaused},
	}
	if err := r.enforcer.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.store.paused["org-1"]) != 0 {
		t.Fatalf("every row is released: %v", r.store.paused["org-1"])
	}
	if _, still := r.store.zero["org-1"]; still {
		t.Fatal("the grace period is over with the credit")
	}
	events := strings.Join(*r.events, ",")
	if !strings.Contains(events, "resume a1") || !strings.Contains(events, "resume p2") {
		t.Fatalf("both services resumed: %s", events)
	}
}

func TestAServiceTheUserResumedOrPausedThemselvesIsNeverStartedByCredit(t *testing.T) {
	r := newRig(OrgState{OrgID: "org-1", Tier: "FREE"})
	r.store.paused["org-1"] = []Paused{
		{Kind: KindApp, ProjectID: "p1", ServiceID: "started-by-user", State: HoldReleased},
		{Kind: KindDatabase, ProjectID: "p2", ServiceID: "p2", State: HoldReleased}, // idle or manual reason now
		{Kind: KindApp, ProjectID: "p3", ServiceID: "deleted", State: HoldGone},
	}
	if err := r.enforcer.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(*r.events, ","), "resume") {
		t.Fatalf("the user's own state wins: %v", *r.events)
	}
	if len(r.store.paused["org-1"]) != 0 {
		t.Fatalf("their rows are dropped: %v", r.store.paused["org-1"])
	}
}

func TestAServiceStillStartingKeepsItsRowForTheNextPass(t *testing.T) {
	r := newRig(OrgState{OrgID: "org-1", Tier: "FREE"})
	r.store.paused["org-1"] = []Paused{{Kind: KindApp, ProjectID: "p1", ServiceID: "a1", State: HoldBusy}}
	if err := r.enforcer.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.store.paused["org-1"]) != 1 || strings.Contains(strings.Join(*r.events, ","), "resume") {
		t.Fatalf("a resume already under way is left alone: %v", *r.events)
	}
}

func TestAResumeThatFailsKeepsItsRowAndTheOthersStillResume(t *testing.T) {
	r := newRig(OrgState{OrgID: "org-1", Tier: "FREE"})
	r.store.paused["org-1"] = []Paused{
		{Kind: KindApp, ProjectID: "p1", ServiceID: "a1", State: HoldPaused},
		{Kind: KindApp, ProjectID: "p1", ServiceID: "a2", State: HoldPaused},
	}
	r.work.resumeErr = map[string]error{"a1": errors.New("no room")}
	if err := r.enforcer.Pass(context.Background()); err == nil {
		t.Fatal("the failure is reported")
	}
	if got := r.store.pausedIDs("org-1"); !slices.Equal(got, []string{"a1"}) {
		t.Fatalf("only the failed one stays for the next pass: %v", got)
	}
}

func TestASuspendedOwnersServicesAreNotResumedByCredit(t *testing.T) {
	r := newRig(OrgState{OrgID: "org-1", Tier: "FREE", OwnerSuspended: true})
	r.store.paused["org-1"] = []Paused{{Kind: KindApp, ProjectID: "p1", ServiceID: "a1", State: HoldPaused}}
	if err := r.enforcer.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(*r.events, ","), "resume") || len(r.store.paused["org-1"]) != 1 {
		t.Fatalf("a suspension outranks credit: %v", *r.events)
	}
}

func TestAnUpgradedOrgIsNoLongerAtZeroSoItsServicesResume(t *testing.T) {
	r := newRig(OrgState{OrgID: "org-1", Tier: "STANDARD", AtZero: false})
	r.store.zero["org-1"] = epoch.Add(-10 * 24 * time.Hour)
	r.store.paused["org-1"] = []Paused{{Kind: KindApp, ProjectID: "p1", ServiceID: "a1", State: HoldPaused}}
	if err := r.enforcer.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.store.paused["org-1"]) != 0 {
		t.Fatal("upgrading releases")
	}
}

func TestRunningOutAgainStartsANewGrace(t *testing.T) {
	r := newRig(OrgState{OrgID: "org-1", Tier: "FREE", AtZero: false})
	r.store.zero["org-1"] = epoch.Add(-10 * 24 * time.Hour)
	if err := r.enforcer.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.store.orgs[0].AtZero = true
	r.store.holdings["org-1"] = holding(2000, 2048, service(KindApp, "a1", 2000, 2048, time.Hour))
	if err := r.enforcer.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.store.paused["org-1"]) != 0 {
		t.Fatal("the first grace is gone, and the second has only begun")
	}
}

func TestAnOrgAtZeroKeepsItsPausedServicesPaused(t *testing.T) {
	r := newRig(zeroOrg(at(-grace)))
	r.store.paused["org-1"] = []Paused{{Kind: KindApp, ProjectID: "p1", ServiceID: "a1", State: HoldPaused}}
	if err := r.enforcer.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(*r.events, ","), "resume") || len(r.store.paused["org-1"]) != 1 {
		t.Fatalf("no credit, no resume: %v", *r.events)
	}
}

func TestAPlanWithNoAllowanceIsNeverPaused(t *testing.T) {
	r := newRig(OrgState{OrgID: "org-1", Tier: "ENTERPRISE", AtZero: true, Since: at(-2 * grace)})
	r.store.holdings["org-1"] = holding(9000, 9000, service(KindApp, "a1", 9000, 9000, time.Hour))
	if err := r.enforcer.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.store.paused["org-1"]) != 0 {
		t.Fatal("a plan the meter does not charge has no free allowance to fall back to")
	}
}

func TestReleaseAllResumesEveryOrgAndClearsEveryGraceWhateverTheirBalance(t *testing.T) {
	r := newRig(zeroOrg(at(-2*grace)), OrgState{OrgID: "org-2", Tier: "FREE", AtZero: true})
	r.store.zero["org-2"] = epoch.Add(-time.Hour)
	r.store.paused["org-1"] = []Paused{{Kind: KindApp, ProjectID: "p1", ServiceID: "a1", State: HoldPaused}}
	r.store.paused["org-2"] = []Paused{{Kind: KindDatabase, ProjectID: "p2", ServiceID: "p2", State: HoldPaused}}
	if err := r.enforcer.ReleaseAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.store.paused["org-1"])+len(r.store.paused["org-2"]) != 0 || len(r.store.zero) != 0 {
		t.Fatalf("turning the flag off lets everything run: paused %v zero %v", r.store.paused, r.store.zero)
	}
}

func TestReleaseAllLeavesASuspendedOwnersServicesForTheirUnsuspend(t *testing.T) {
	r := newRig(OrgState{OrgID: "org-1", Tier: "FREE", AtZero: true, OwnerSuspended: true})
	r.store.paused["org-1"] = []Paused{{Kind: KindApp, ProjectID: "p1", ServiceID: "a1", State: HoldPaused}}
	if err := r.enforcer.ReleaseAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.store.paused["org-1"]) != 1 {
		t.Fatal("kept")
	}
}

func TestServicesOfTheSameAgePauseInAFixedOrder(t *testing.T) {
	r := newRig(zeroOrg(at(-grace)))
	r.store.holdings["org-1"] = holding(2000, 2048,
		service(KindApp, "a", 500, 512, time.Hour),
		service(KindApp, "b", 500, 512, time.Hour),
		service(KindApp, "c", 500, 512, time.Hour),
		service(KindApp, "d", 500, 512, time.Hour))
	if err := r.enforcer.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.store.pausedIDs("org-1"); !slices.Equal(got, []string{"c", "d"}) {
		t.Fatalf("%v", got)
	}
	first := r.store.pausedIDs("org-1")
	again := newRig(zeroOrg(at(-grace)))
	again.store.holdings["org-1"] = r.store.holdings["org-1"]
	_ = again.enforcer.Pass(context.Background())
	if !slices.Equal(first, again.store.pausedIDs("org-1")) {
		t.Fatal("the same org pauses the same services every time")
	}
}

func TestAtZeroRowsOfGoneOrUserOwnedServicesAreDroppedEvenInTheGrace(t *testing.T) {
	for name, since := range map[string]time.Duration{"in the grace": -time.Hour, "after it": -2 * grace} {
		r := newRig(zeroOrg(at(since)))
		r.store.paused["org-1"] = []Paused{
			{Kind: KindApp, ProjectID: "p1", ServiceID: "gone", State: HoldGone},
			{Kind: KindApp, ProjectID: "p1", ServiceID: "user-owned", State: HoldReleased},
			{Kind: KindApp, ProjectID: "p1", ServiceID: "still-paused", State: HoldPaused},
		}
		if err := r.enforcer.Pass(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := r.store.pausedIDs("org-1"); !slices.Equal(got, []string{"still-paused"}) {
			t.Errorf("%s: %v", name, got)
		}
	}
}

type fakeLeader struct {
	leading bool
	err     error
}

func (l fakeLeader) IsLeader(context.Context) (bool, error) { return l.leading, l.err }

func TestATickEnforcesWhileMeteringIsOnAndReleasesWhileItIsOff(t *testing.T) {
	setup := func() *rig {
		r := newRig(zeroOrg(at(-grace)))
		r.store.holdings["org-1"] = holding(2000, 2048, service(KindApp, "a1", 2000, 2048, time.Hour))
		return r
	}
	on := setup()
	on.enforcer.Tick(context.Background(), fakeLeader{leading: true}, func(context.Context) bool { return true })
	if len(on.store.paused["org-1"]) != 1 {
		t.Fatalf("metering on: %v", *on.events)
	}
	off := setup()
	off.store.paused["org-1"] = []Paused{{Kind: KindApp, ProjectID: "p1", ServiceID: "held", State: HoldPaused}}
	off.enforcer.Tick(context.Background(), fakeLeader{leading: true}, func(context.Context) bool { return false })
	if len(off.store.paused["org-1"]) != 0 || strings.Contains(strings.Join(*off.events, ","), "pause a1") {
		t.Fatalf("metering off releases and pauses nothing: %v", *off.events)
	}
}

func TestATickOnAReplicaThatDoesNotLeadDoesNothing(t *testing.T) {
	r := newRig(zeroOrg(at(-grace)))
	r.store.holdings["org-1"] = holding(2000, 2048, service(KindApp, "a1", 2000, 2048, time.Hour))
	for name, leader := range map[string]fakeLeader{"follower": {leading: false}, "unknown": {err: errors.New("lock lost")}} {
		r.enforcer.Tick(context.Background(), leader, func(context.Context) bool { return true })
		if len(*r.events) != 0 {
			t.Fatalf("%s acted: %v", name, *r.events)
		}
	}
}
