package config

import "testing"

var meterEnvs = []string{
	"CREDIT_PRICES",
	"CREDIT_GRACE_DAYS",
}

func clearMeterEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CORS_ORIGINS", "https://app.test")
	for _, env := range meterEnvs {
		t.Setenv(env, "")
	}
}

func TestMeterDefaultsAreThePlansPrices(t *testing.T) {
	clearMeterEnv(t)
	got := Load().CreditMeter
	got.err = nil
	want := CreditMeterConfig{
		PriceVersion: "v1", VCPUMonthMicros: 12_000_000, RAMGiBMonthMicros: 6_000_000, DiskGiBMonthMicros: 120_000, GraceDays: 7,
	}
	if got != want {
		t.Errorf("defaults:\n got %+v\nwant %+v", got, want)
	}
	if err := Load().Validate(); err != nil {
		t.Errorf("defaults do not validate: %v", err)
	}
}

func TestMeterPricesCanBeChanged(t *testing.T) {
	clearMeterEnv(t)
	t.Setenv("CREDIT_PRICES", `{"version":"v2","vcpuMonthMicros":10000000,"ramGiBMonthMicros":5000000,"diskGiBMonthMicros":100000}`)
	got := Load().CreditMeter
	if got.PriceVersion != "v2" || got.VCPUMonthMicros != 10_000_000 || got.RAMGiBMonthMicros != 5_000_000 || got.DiskGiBMonthMicros != 100_000 {
		t.Fatalf("prices %+v", got)
	}
	if err := Load().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestABadPriceStopsTheBoot(t *testing.T) {
	for name, set := range map[string]func(*testing.T){
		"not json": func(t *testing.T) { t.Setenv("CREDIT_PRICES", "12") },
		"negative price": func(t *testing.T) {
			t.Setenv("CREDIT_PRICES", `{"version":"v","vcpuMonthMicros":-1,"ramGiBMonthMicros":1,"diskGiBMonthMicros":1}`)
		},
		"no version": func(t *testing.T) {
			t.Setenv("CREDIT_PRICES", `{"vcpuMonthMicros":1,"ramGiBMonthMicros":1,"diskGiBMonthMicros":1}`)
		},
		"missing price": func(t *testing.T) { t.Setenv("CREDIT_PRICES", `{"version":"v","vcpuMonthMicros":1}`) },
	} {
		clearMeterEnv(t)
		set(t)
		if Load().Validate() == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestTheGracePeriodIsSevenDaysAndCanBeChanged(t *testing.T) {
	clearMeterEnv(t)
	if got := Load().CreditMeter.GraceDays; got != 7 {
		t.Fatalf("default %d", got)
	}
	t.Setenv("CREDIT_GRACE_DAYS", "0")
	if got := Load().CreditMeter.GraceDays; got != 0 || Load().Validate() != nil {
		t.Fatalf("zero days means pausing at once: %d", got)
	}
	t.Setenv("CREDIT_GRACE_DAYS", "14")
	if got := Load().CreditMeter.GraceDays; got != 14 {
		t.Fatalf("set %d", got)
	}
}

func TestABadGracePeriodStopsTheBoot(t *testing.T) {
	for _, bad := range []string{"-1", "seven", "1.5", "4000"} {
		clearMeterEnv(t)
		t.Setenv("CREDIT_GRACE_DAYS", bad)
		if Load().Validate() == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
