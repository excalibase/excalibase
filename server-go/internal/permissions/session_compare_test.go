package permissions

import (
	"encoding/json"
	"testing"
)

// EXC-680: a permission may compare a session variable with a fixed value,
// e.g. require aal2, with the same rules as the engine's parser.
func TestValidateBoolExp_AcceptsASessionVariableAgainstAFixedValue(t *testing.T) {
	for _, exp := range []string{
		`{"X-Excalibase-Aal":{"_eq":"aal2"}}`,
		`{"x-excalibase-aal":{"_neq":"aal1"}}`,
		`{"X-Excalibase-Aal":{"_in":["aal2","aal3"]},"X-Excalibase-Team":{"_nin":[]}}`,
		`{"X-Excalibase-Aal":{"_is_null":false}}`,
		`{"_or":[{"X-Excalibase-Aal":{"_eq":"aal2"}},{"public":{"_eq":true}}]}`,
		`{"author":{"X-Excalibase-tenant_id":{"_eq":"acme"}}}`,
	} {
		if err := ValidateBoolExp(json.RawMessage(exp)); err != nil {
			t.Errorf("%s must be accepted: %v", exp, err)
		}
	}
}

func TestValidateBoolExp_RefusesWhatASessionComparisonDoesNotTake(t *testing.T) {
	for _, exp := range []string{
		`{"X-Excalibase-Aal":{"_gt":"aal1"}}`,
		`{"X-Excalibase-Aal":{"_like":"aal%"}}`,
		`{"X-Excalibase-Aal":{"_eq":2}}`,
		`{"X-Excalibase-Aal":{"_eq":true}}`,
		`{"X-Excalibase-Aal":{"_eq":null}}`,
		`{"X-Excalibase-Aal":{"_eq":"X-Excalibase-User-Id"}}`,
		`{"X-Excalibase-Aal":{"_in":["aal2",3]}}`,
		`{"X-Excalibase-Aal":{"_in":"X-Excalibase-Levels"}}`,
		`{"X-Excalibase-Aal":{"_is_null":"yes"}}`,
		`{"X-Excalibase-Aal":{}}`,
		`{"X-Excalibase-Aal":{"level":{"_eq":"aal2"}}}`,
		`{"X-Excalibase-":{"_eq":"aal2"}}`,
		`{"X-Excalibase-A al":{"_eq":"aal2"}}`,
	} {
		if err := ValidateBoolExp(json.RawMessage(exp)); err == nil {
			t.Errorf("%s must be refused", exp)
		}
	}
}

// The live schema check leaves a session variable alone: it names no column.
func TestCheckLive_LeavesSessionComparisonsToTheRequest(t *testing.T) {
	source := sourceOf(cornerShop("app", "app"))
	body := `{"filter":{"X-Excalibase-Aal":{"_eq":"aal2"},"status":{"_eq":"paid"}},"columns":"*"}`
	if reason := checkReason(t, source, "app.orders", "select", body); reason != "" {
		t.Fatalf("refused: %s", reason)
	}
}
