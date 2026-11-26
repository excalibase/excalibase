package credits

import "testing"

func TestMoneyFormatsWholeAndFractionalDollars(t *testing.T) {
	cases := map[Micros]string{
		0:               "0.00",
		300_000_000:     "300.00",
		4110:            "0.00411",
		1_500_000:       "1.50",
		-2_250_000:      "-2.25",
		1_000_001:       "1.000001",
		123_456_789_000: "123456.789",
		-4110:           "-0.00411",
	}
	for micros, want := range cases {
		if got := micros.Dollars(); got != want {
			t.Errorf("%d micros = %q, want %q", int64(micros), got, want)
		}
	}
}

func TestFromDollarsIsExact(t *testing.T) {
	if FromDollars(300) != 300_000_000 {
		t.Fatalf("300 dollars = %d", int64(FromDollars(300)))
	}
}
