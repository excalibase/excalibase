package service

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
)

// fakeNamespacePlans records the plan labels provisioning writes.
type fakeNamespacePlans struct {
	mu     sync.Mutex
	plans  map[string]string
	writes []string
	fail   error
}

func (f *fakeNamespacePlans) ProjectNamespacePlans(context.Context) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	out := map[string]string{}
	for ns, plan := range f.plans {
		out[ns] = plan
	}
	return out, nil
}

func (f *fakeNamespacePlans) SetNamespacePlan(_ context.Context, namespace, plan string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.writes = append(f.writes, namespace+"="+plan)
	f.plans[namespace] = plan
	return nil
}

var testLogRetention = config.LogRetention{
	Default: 7 * 24 * time.Hour,
	ByPlan:  map[domain.TierType]time.Duration{domain.Free: 24 * time.Hour, domain.Standard: 7 * 24 * time.Hour},
}

func withLogRetention(svc *ProvisioningService, on bool, plans *fakeNamespacePlans) {
	flags := features.NewStatic()
	if on {
		flags = features.NewStatic(features.LogRetention)
	}
	svc.SetLogRetention(flags, testLogRetention, plans)
}

func searchedBack(t *testing.T, loki *fakeLoki, before time.Time) time.Duration {
	t.Helper()
	return before.Sub(time.Unix(0, loki.starts[len(loki.starts)-1]))
}

// EXC-659: a Free project's search never reaches past the day Loki keeps.
func TestLogSearchIsClampedToThePlansRetention(t *testing.T) {
	svc, _, _ := setupOpsTest(t)
	withLogRetention(svc, true, &fakeNamespacePlans{plans: map[string]string{}})
	loki := &fakeLoki{lines: map[string][]string{testOpsDBNS: {"one"}}}
	server := httptest.NewServer(loki)
	defer server.Close()
	svc.SetLokiURL(server.URL)

	before := time.Now()
	page, err := svc.SearchProjectLogs(context.Background(), testOpsDB, LogSearch{Lines: 10, Since: 7 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if got := searchedBack(t, loki, before); got > 24*time.Hour+time.Minute {
		t.Errorf("a Free project searched back %s", got)
	}
	if page.KeptFor != 24*time.Hour || page.Lines != "one\n" {
		t.Errorf("page %+v", page)
	}
}

func TestLogSearchKeepsTheAskedRangeWithTheFlagOff(t *testing.T) {
	svc, _, _ := setupOpsTest(t)
	plans := &fakeNamespacePlans{plans: map[string]string{}}
	withLogRetention(svc, false, plans)
	loki := &fakeLoki{lines: map[string][]string{testOpsDBNS: {"one"}}}
	server := httptest.NewServer(loki)
	defer server.Close()
	svc.SetLokiURL(server.URL)

	before := time.Now()
	page, err := svc.SearchProjectLogs(context.Background(), testOpsDB, LogSearch{Lines: 10, Since: 7 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if got := searchedBack(t, loki, before); got < 7*24*time.Hour-time.Minute {
		t.Errorf("flag off searched back %s, want the 7 days asked", got)
	}
	if page.KeptFor != 0 {
		t.Errorf("flag off reports retention %s", page.KeptFor)
	}
}

// The sweep labels every project namespace with its plan, writing only what differs.
func TestSyncLogPlansLabelsEachProjectNamespaceWithItsPlan(t *testing.T) {
	svc, store, _ := setupOpsTest(t)
	for _, inst := range []*domain.DatabaseInstance{
		{ProjectID: "proj-paid", OrgID: "org2", Namespace: "org2-proj-paid", Tier: domain.Standard, Status: "ACTIVE"},
		{ProjectID: "proj-host", OrgID: "org3", Namespace: "", Tier: domain.Free, Status: "ACTIVE", DeploymentMode: domain.ModeDocker},
		{ProjectID: "proj-gone", OrgID: "org4", Namespace: "org4-proj-gone", Tier: domain.Free, Status: "ACTIVE"},
	} {
		if err := store.Create(inst); err != nil {
			t.Fatal(err)
		}
	}
	plans := &fakeNamespacePlans{plans: map[string]string{testOpsDBNS: "free", "org2-proj-paid": ""}}
	withLogRetention(svc, true, plans)

	written, err := svc.SyncLogPlans(context.Background())
	if err != nil || written != 1 || len(plans.writes) != 1 || plans.writes[0] != "org2-proj-paid=standard" {
		t.Fatalf("written %d err %v writes %v", written, err, plans.writes)
	}
	if written, _ := svc.SyncLogPlans(context.Background()); written != 0 {
		t.Errorf("second sweep wrote %d labels", written)
	}
}

func TestSyncLogPlansWritesNothingWithTheFlagOff(t *testing.T) {
	svc, _, _ := setupOpsTest(t)
	plans := &fakeNamespacePlans{plans: map[string]string{testOpsDBNS: ""}}
	withLogRetention(svc, false, plans)
	if written, err := svc.SyncLogPlans(context.Background()); err != nil || written != 0 || len(plans.writes) != 0 {
		t.Fatalf("flag off: written %d err %v writes %v", written, err, plans.writes)
	}
}

func TestSyncLogPlansReportsWhatItCouldNotRead(t *testing.T) {
	svc, _, _ := setupOpsTest(t)
	withLogRetention(svc, true, &fakeNamespacePlans{plans: map[string]string{}, fail: errors.New("forbidden")})
	if _, err := svc.SyncLogPlans(context.Background()); err == nil {
		t.Fatal("a failed namespace read reported success")
	}
}

// A plan change relabels the namespace at once: new lines take the new
// plan's retention, lines already stored keep the one they were stored with.
func TestApplyOrgTierRelabelsTheProjectsLogs(t *testing.T) {
	svc, _, mock := setupClusterChangeTest(t)
	plans := &fakeNamespacePlans{plans: map[string]string{testOpsDBNS: "free"}}
	withLogRetention(svc, true, plans)
	setOrgTier(svc, "org1", domain.Standard)
	mock.Capacity = threeNodes()
	if err := svc.ApplyOrgTier(context.Background(), testOpsDB, domain.Standard); err != nil {
		t.Fatalf("ApplyOrgTier: %v", err)
	}
	if plans.plans[testOpsDBNS] != "standard" {
		t.Errorf("namespace plan %q after moving to STANDARD", plans.plans[testOpsDBNS])
	}
}

// The label is the log store's business: the plan change stands without it,
// and the sweep writes it later.
func TestApplyOrgTierStandsWhenTheLabelCannotBeWritten(t *testing.T) {
	svc, store, mock := setupClusterChangeTest(t)
	withLogRetention(svc, true, &fakeNamespacePlans{plans: map[string]string{}, fail: errors.New("forbidden")})
	setOrgTier(svc, "org1", domain.Standard)
	mock.Capacity = threeNodes()
	if err := svc.ApplyOrgTier(context.Background(), testOpsDB, domain.Standard); err != nil {
		t.Fatalf("ApplyOrgTier: %v", err)
	}
	if inst, _ := store.FindByProjectID(testOpsDB); inst.Tier != domain.Standard {
		t.Errorf("tier %s", inst.Tier)
	}
}

func TestApplyOrgTierLeavesTheLabelWithTheFlagOff(t *testing.T) {
	svc, _, mock := setupClusterChangeTest(t)
	plans := &fakeNamespacePlans{plans: map[string]string{testOpsDBNS: ""}}
	withLogRetention(svc, false, plans)
	setOrgTier(svc, "org1", domain.Standard)
	mock.Capacity = threeNodes()
	if err := svc.ApplyOrgTier(context.Background(), testOpsDB, domain.Standard); err != nil {
		t.Fatalf("ApplyOrgTier: %v", err)
	}
	if len(plans.writes) != 0 {
		t.Errorf("flag off wrote %v", plans.writes)
	}
}

// The sweep labels at once, not only after its first interval.
func TestRunLogPlanSweepLabelsOnStartAndStopsWithItsContext(t *testing.T) {
	svc, _, _ := setupOpsTest(t)
	plans := &fakeNamespacePlans{plans: map[string]string{testOpsDBNS: ""}}
	withLogRetention(svc, true, plans)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		svc.RunLogPlanSweep(ctx, time.Hour)
		close(done)
	}()
	deadline := time.After(5 * time.Second)
	for {
		plans.mu.Lock()
		labelled := plans.plans[testOpsDBNS] == "free"
		plans.mu.Unlock()
		if labelled {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the sweep never labelled the namespace")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweep outlived its context")
	}
}

func TestRunLogPlanSweepKeepsGoingWhenAReadFails(t *testing.T) {
	svc, _, _ := setupOpsTest(t)
	withLogRetention(svc, true, &fakeNamespacePlans{plans: map[string]string{}, fail: errors.New("forbidden")})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	svc.RunLogPlanSweep(ctx, 10*time.Millisecond)
}
