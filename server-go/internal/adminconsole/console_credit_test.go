package adminconsole

import (
	"context"
	"errors"
	"testing"
)

type orgsReader struct {
	fakeReader
	orgs []OrgRow
}

func (r *orgsReader) ListOrgs(context.Context, OrgQuery) (Page[OrgRow], error) {
	return Page[OrgRow]{Items: append([]OrgRow(nil), r.orgs...), Total: len(r.orgs)}, nil
}

func newOrgConsole(orgs ...OrgRow) *Console {
	return NewConsole(&orgsReader{orgs: orgs}, nil, nil)
}

func TestOrgsCarryTheirCreditBalanceWhenAProviderIsWired(t *testing.T) {
	console := newOrgConsole(OrgRow{ID: "o1"}, OrgRow{ID: "o2"}, OrgRow{ID: "o3"})
	var asked []string
	console.SetCreditBalances(func(_ context.Context, orgIDs []string) (map[string]int64, error) {
		asked = orgIDs
		return map[string]int64{"o1": 300_000_000, "o3": 0}, nil
	})
	page, err := console.Orgs(context.Background(), OrgQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 3 {
		t.Fatalf("asked for %v", asked)
	}
	if page.Items[0].CreditMicros == nil || *page.Items[0].CreditMicros != 300_000_000 {
		t.Fatalf("o1: %v", page.Items[0].CreditMicros)
	}
	if page.Items[1].CreditMicros != nil {
		t.Fatal("an org with no account has no balance shown")
	}
	if page.Items[2].CreditMicros == nil || *page.Items[2].CreditMicros != 0 {
		t.Fatal("a zero balance is shown as zero")
	}
}

func TestOrgsWithoutAProviderOrWithItSwitchedOffHaveNoCreditField(t *testing.T) {
	console := newOrgConsole(OrgRow{ID: "o1"})
	page, _ := console.Orgs(context.Background(), OrgQuery{})
	if page.Items[0].CreditMicros != nil {
		t.Fatal("no provider wired")
	}
	console.SetCreditBalances(func(context.Context, []string) (map[string]int64, error) { return nil, nil })
	page, _ = console.Orgs(context.Background(), OrgQuery{})
	if page.Items[0].CreditMicros != nil {
		t.Fatal("a provider that answers nothing (flag off)")
	}
}

func TestACreditLookupFailureDoesNotHideTheOrgList(t *testing.T) {
	console := newOrgConsole(OrgRow{ID: "o1"})
	console.SetCreditBalances(func(context.Context, []string) (map[string]int64, error) { return nil, errors.New("down") })
	page, err := console.Orgs(context.Background(), OrgQuery{})
	if err != nil || len(page.Items) != 1 || page.Items[0].CreditMicros != nil {
		t.Fatalf("%v %+v", err, page)
	}
}
