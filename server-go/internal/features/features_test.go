package features

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStatic_EveryFeatureIsOffUnlessSwitchedOn(t *testing.T) {
	flags := NewStatic()
	for _, feature := range All() {
		if flags.Enabled(context.Background(), feature) {
			t.Errorf("%s must default to off", feature)
		}
	}
}

func TestStatic_OnlyTheNamedFeaturesAreOn(t *testing.T) {
	flags := NewStatic(MCP)
	if !flags.Enabled(context.Background(), MCP) {
		t.Error("mcp was switched on")
	}
	if flags.Enabled(context.Background(), Pipeline) {
		t.Error("pipeline was not switched on")
	}
}

func TestStatic_AnUndeclaredFeatureIsNeverOn(t *testing.T) {
	if NewStatic(Feature("timetravel")).Enabled(context.Background(), Feature("timetravel")) {
		t.Error("only declared features can be switched on")
	}
}

func TestEnabled_NilFlagsMeanEverythingOff(t *testing.T) {
	if Enabled(context.Background(), nil, MCP) {
		t.Error("no flags means no new features")
	}
}

func TestAll_NamesAreUniqueAndLowercase(t *testing.T) {
	seen := map[Feature]bool{}
	for _, feature := range All() {
		if seen[feature] {
			t.Errorf("%s declared twice", feature)
		}
		seen[feature] = true
		for _, c := range feature {
			if c < 'a' || c > 'z' {
				t.Errorf("%s: feature names are lowercase words, as Studio reads them", feature)
			}
		}
	}
	if !seen[MCP] || !seen[Pipeline] {
		t.Error("mcp and pipeline are declared")
	}
}

func TestSnapshot_ReportsEveryDeclaredFeature(t *testing.T) {
	got := Snapshot(context.Background(), NewStatic(Pipeline))
	if len(got) != len(All()) {
		t.Fatalf("snapshot names %d features, %d are declared", len(got), len(All()))
	}
	if got[string(MCP)] || !got[string(Pipeline)] {
		t.Errorf("snapshot: %v", got)
	}
}

func TestRequire_AnswersNotFoundWhenOff(t *testing.T) {
	reached := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })
	rec := httptest.NewRecorder()
	Require(NewStatic(), MCP)(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if rec.Code != http.StatusNotFound || reached {
		t.Fatalf("off: %d, reached=%v", rec.Code, reached)
	}
}

func TestRequire_PassesThroughWhenOn(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	rec := httptest.NewRecorder()
	Require(NewStatic(MCP), MCP)(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("on: %d", rec.Code)
	}
}
