package adminconsole

import (
	"context"
	"errors"
	"testing"
)

func TestOrgsCarryTheirPlanBudgetWhenAProviderIsWired(t *testing.T) {
	console := newOrgConsole(OrgRow{ID: "o1"}, OrgRow{ID: "o2"})
	console.SetPlanBudgets(func(_ context.Context, orgIDs []string) (map[string]OrgBudget, error) {
		return map[string]OrgBudget{"o1": {CPUMilliUsed: 500, CPUMilliLimit: 1000, MemoryBytesUsed: 1 << 30, MemoryBytesLimit: 2 << 30}}, nil
	})
	page, err := console.Orgs(context.Background(), OrgQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if got := page.Items[0].Budget; got == nil || got.CPUMilliUsed != 500 || got.MemoryBytesLimit != 2<<30 {
		t.Fatalf("o1: %+v", got)
	}
	if page.Items[1].Budget != nil {
		t.Fatal("an org the provider does not know shows no budget")
	}
}

func TestOrgsHaveNoBudgetWithoutAProvider(t *testing.T) {
	page, _ := newOrgConsole(OrgRow{ID: "o1"}).Orgs(context.Background(), OrgQuery{})
	if page.Items[0].Budget != nil {
		t.Fatal("no provider wired")
	}
}

func TestABudgetLookupFailureDoesNotHideTheOrgList(t *testing.T) {
	console := newOrgConsole(OrgRow{ID: "o1"})
	console.SetPlanBudgets(func(context.Context, []string) (map[string]OrgBudget, error) { return nil, errors.New("down") })
	page, err := console.Orgs(context.Background(), OrgQuery{})
	if err != nil || len(page.Items) != 1 || page.Items[0].Budget != nil {
		t.Fatalf("%v %+v", err, page)
	}
}
