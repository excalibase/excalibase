package main

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/creditmeter"
	"github.com/excalibase/provisioning-poc/internal/credits"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/planbudget"
)

func burnConfig(mode string, flags ...features.Feature) config.AppConfig {
	return config.AppConfig{DeploymentMode: mode, DarkFeatures: flags, CreditMeter: config.CreditMeterConfig{
		PriceVersion: "v7", VCPUMonthMicros: 12_000_000, RAMGiBMonthMicros: 6_000_000, DiskGiBMonthMicros: 120_000,
	}, PlanBudget: config.PlanBudgetConfig{
		Free:     config.PlanNumbers{AllowanceCPUMilli: 1000, AllowanceMemoryMiB: 1024, AllowanceDiskGiB: 10},
		Standard: config.PlanNumbers{AllowanceDiskGiB: 2},
	}}
}

func TestMeteringRunsOnlyOnACloudInstallWithTheFlagOn(t *testing.T) {
	for name, c := range map[string]struct {
		cfg  config.AppConfig
		want bool
	}{
		"flag off":             {burnConfig("cloud"), false},
		"flag on, cloud":       {burnConfig("cloud", features.CreditBurn), true},
		"flag on, self-hosted": {burnConfig("selfhosted", features.CreditBurn), false},
	} {
		if got := creditBurnOn(c.cfg); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

func TestBurningCreditNeedsTheLedgerAndTheBudget(t *testing.T) {
	cases := []struct {
		name  string
		flags []features.Feature
		fails bool
	}{
		{"all off", nil, false},
		{"credits only", []features.Feature{features.Credits}, false},
		{"burn alone", []features.Feature{features.CreditBurn}, true},
		{"burn without the budget", []features.Feature{features.CreditBurn, features.Credits}, true},
		{"burn without credits", []features.Feature{features.CreditBurn, features.PlanBudget}, true},
		{"burn with both", []features.Feature{features.CreditBurn, features.Credits, features.PlanBudget}, false},
	}
	for _, c := range cases {
		err := creditBurnBootCheck(burnConfig("cloud", c.flags...))
		if (err != nil) != c.fails {
			t.Errorf("%s: err %v, want failure %v", c.name, err, c.fails)
		}
	}
}

func TestTheBootCheckFailsBeforeTheCreditsOffShortcut(t *testing.T) {
	err := creditsBootCheck(burnConfig("cloud", features.CreditBurn))
	if err == nil {
		t.Fatal("burning credit with credits off must refuse to boot")
	}
}

func TestSelfHostedIgnoresTheBurnFlag(t *testing.T) {
	if err := creditBurnBootCheck(burnConfig("selfhosted", features.CreditBurn)); err != nil {
		t.Fatalf("a self-hosted install is never metered, so it asks nothing: %v", err)
	}
}

func TestPricesComeFromConfig(t *testing.T) {
	prices := creditPrices(burnConfig("cloud"))
	if prices.Version != "v7" || prices.VCPUMonth != credits.FromDollars(12) || prices.RAMGiBMonth != credits.FromDollars(6) || prices.DiskGiBMonth != 120_000 {
		t.Fatalf("%+v", prices)
	}
}

func TestTheAllowanceIsTheOneInThePlanBudget(t *testing.T) {
	allow := creditAllowance(burnConfig("cloud"))
	if got, ok := allow("free"); !ok || got != (creditmeter.Allowance{CPUMilli: 1000, MemoryBytes: 1 << 30, DiskBytes: 10 << 30}) {
		t.Fatalf("free allowance %+v %v", got, ok)
	}
	if got, ok := allow("STANDARD"); !ok || got.DiskBytes != 2<<30 || got.CPUMilli != 0 {
		t.Fatalf("standard allowance %+v %v", got, ok)
	}
}

func TestAPlanThatIsNotFreeOrStandardIsNotMetered(t *testing.T) {
	allow := creditAllowance(burnConfig("cloud"))
	for _, tier := range []string{"ENTERPRISE", "", "mystery"} {
		if got, ok := allow(tier); ok {
			t.Errorf("plan %q must be skipped, not charged at a zero allowance: %+v", tier, got)
		}
	}
}

type fakeAllowanceStore struct {
	lookup func(string) (creditmeter.Allowance, bool)
}

func (f *fakeAllowanceStore) SetCreditAllowance(lookup func(string) (creditmeter.Allowance, bool)) {
	f.lookup = lookup
}

func TestTheStoreIsGivenTheAllowancesOnlyWhileMetering(t *testing.T) {
	off, on := &fakeAllowanceStore{}, &fakeAllowanceStore{}
	setCreditAllowance(burnConfig("cloud"), off)
	setCreditAllowance(burnConfig("cloud", features.CreditBurn), on)
	if off.lookup != nil || on.lookup == nil {
		t.Fatalf("off %v on %v", off.lookup != nil, on.lookup != nil)
	}
}

func TestLotsWaitForTheMeterOnlyWhileMeteringIsOn(t *testing.T) {
	if creditRules(burnConfig("cloud")).Metered {
		t.Fatal("with metering off a lot expires by the clock")
	}
	if !creditRules(burnConfig("cloud", features.CreditBurn)).Metered {
		t.Fatal("with metering on a lot expires once the watermark passes it")
	}
}

func TestTheMeterStartsNothingWhileOff(t *testing.T) {
	stop := startCreditMeter(burnConfig("cloud"), nil, nil)
	stop()
}

type fakeObserving struct{ orgs []string }

func (f *fakeObserving) ObserveOrgUsage(_ context.Context, _ planbudget.Querier, orgID string) {
	f.orgs = append(f.orgs, orgID)
}

func TestTheGateIsGivenTheSamplingObserverOnlyWhileMetering(t *testing.T) {
	store := &fakeObserving{}
	if creditObserver(burnConfig("cloud"), store) != nil {
		t.Fatal("no observer while metering is off")
	}
	if creditObserver(burnConfig("cloud", features.CreditBurn), struct{}{}) != nil {
		t.Fatal("a store that cannot sample gets no observer")
	}
	observer := creditObserver(burnConfig("cloud", features.CreditBurn), store)
	if observer == nil {
		t.Fatal("metering on: the gate must sample before each step")
	}
	observer(context.Background(), nil, "org-9")
	if len(store.orgs) != 1 || store.orgs[0] != "org-9" {
		t.Fatalf("the observer must sample the org of the step: %v", store.orgs)
	}
}
