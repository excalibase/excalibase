package credits

import (
	"strings"
	"testing"
	"time"
)

func TestStatementCSVNeutralisesFormulasAndKeepsNegativeAmounts(t *testing.T) {
	at := time.Date(2026, 12, 3, 4, 5, 6, 0, time.UTC)
	var out strings.Builder
	err := WriteStatementCSV(&out, Statement{Month: "2026-12", Opening: 1_000_000, Closing: 500_000, Entries: []Entry{
		{CreatedAt: at, Kind: KindAdjustAdmin, Amount: -500_000, Reason: `=HYPERLINK("http://evil")`},
		{CreatedAt: at, Kind: KindAdjustAdmin, Amount: 0, Reason: "-cmd|calc"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"time_utc,kind,amount_usd", "2026-12-01,opening_balance,1.00", "-0.50", `'=HYPERLINK`, "'-cmd|calc", ",closing_balance,0.50"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	if strings.Contains(text, "'-0.50") {
		t.Error("a negative amount must stay a number")
	}
}
