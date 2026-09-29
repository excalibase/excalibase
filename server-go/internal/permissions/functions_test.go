package permissions

import (
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/schema"
)

func searchOrders(mutate func(*schema.FunctionDetail)) []schema.FunctionDetail {
	detail := schema.FunctionDetail{
		Schema: "public", Name: "search_orders", Kind: "f", Volatility: "STABLE",
		ReturnsSet: true, ReturnsTable: "public.orders",
		Args: []schema.FunctionArg{{Name: "term", Type: "text"}, {Name: "session", Type: "jsonb"}, {Name: "legacy", Type: "json"}},
	}
	if mutate != nil {
		mutate(&detail)
	}
	return []schema.FunctionDetail{detail}
}

func strPtr(s string) *string { return &s }

func TestTrackable_ExposureFollowsVolatility(t *testing.T) {
	for volatility, want := range map[string]string{
		"STABLE": domain.ExposeAsQuery, "IMMUTABLE": domain.ExposeAsQuery, "VOLATILE": domain.ExposeAsMutation,
	} {
		decision, err := Trackable(searchOrders(func(d *schema.FunctionDetail) { d.Volatility = volatility }), nil)
		if err != nil {
			t.Fatalf("%s: %v", volatility, err)
		}
		if decision.ExposedAs != want {
			t.Errorf("%s exposed as %s, want %s", volatility, decision.ExposedAs, want)
		}
	}
}

func TestTrackable_SecurityDefinerIsAllowedAndReported(t *testing.T) {
	decision, err := Trackable(searchOrders(func(d *schema.FunctionDetail) { d.SecurityDefiner = true }), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.SecurityDefiner {
		t.Fatal("a SECURITY DEFINER function must be reported so Studio can warn")
	}
}

func TestTrackable_SessionArgument(t *testing.T) {
	for _, name := range []string{"session", "legacy"} {
		if _, err := Trackable(searchOrders(nil), strPtr(name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, name := range []string{"term", "missing", ""} {
		if _, err := Trackable(searchOrders(nil), strPtr(name)); err == nil {
			t.Errorf("session argument %q must be refused", name)
		}
	}
}

func TestTrackable_Refusals(t *testing.T) {
	two := append(searchOrders(nil), searchOrders(nil)...)
	cases := []struct {
		name     string
		details  []schema.FunctionDetail
		contains string
	}{
		{"procedure", searchOrders(func(d *schema.FunctionDetail) { d.Kind = "p" }), "procedure"},
		{"aggregate", searchOrders(func(d *schema.FunctionDetail) { d.Kind = "a" }), "aggregate"},
		{"overloaded", two, "overloaded"},
		{"returns a scalar", searchOrders(func(d *schema.FunctionDetail) { d.ReturnsTable = "" }), "table or view"},
		{"returns a reserved table", searchOrders(func(d *schema.FunctionDetail) { d.ReturnsTable = "auth.users" }), "served"},
		{"returns a mixed-case table", searchOrders(func(d *schema.FunctionDetail) { d.ReturnsTable = "public.Orders" }), "lower-case"},
		{"lives in a reserved schema", searchOrders(func(d *schema.FunctionDetail) { d.Schema = "excalibase" }), "served"},
		{"mixed-case name", searchOrders(func(d *schema.FunctionDetail) { d.Name = "searchOrders" }), "lower-case"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Trackable(c.details, nil)
			if err == nil || !strings.Contains(err.Error(), c.contains) {
				t.Fatalf("got %v, want an error mentioning %q", err, c.contains)
			}
		})
	}
}

func TestTrackable_NotFound(t *testing.T) {
	if _, err := Trackable(nil, nil); !errors.Is(err, ErrFunctionNotFound) {
		t.Fatalf("got %v, want ErrFunctionNotFound", err)
	}
}

func TestIsServedSchema(t *testing.T) {
	for _, served := range []string{"public", "sales", "excalibase_app"} {
		if !IsServedSchema(served) {
			t.Errorf("%s must be served", served)
		}
	}
	for _, reserved := range []string{"auth", "excalibase", "information_schema", "pg_catalog", "pg_toast", "AUTH"} {
		if IsServedSchema(reserved) {
			t.Errorf("%s must not be served", reserved)
		}
	}
}
