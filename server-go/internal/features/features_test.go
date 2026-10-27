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

func TestWhenOnAppliesTheMiddlewareOnlyWhileTheFeatureIsOn(t *testing.T) {
	refuse := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	for _, tc := range []struct {
		flags Flags
		want  int
	}{{NewStatic(AuthOAuth), http.StatusForbidden}, {NewStatic(), http.StatusOK}} {
		w := httptest.NewRecorder()
		WhenOn(tc.flags, AuthOAuth, refuse)(ok).ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/", nil))
		if w.Code != tc.want {
			t.Errorf("status = %d, want %d", w.Code, tc.want)
		}
	}
}

func TestWhenAnyOnAppliesTheMiddlewareWhileEitherFeatureIsOn(t *testing.T) {
	refuse := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	either := []Feature{AuthOAuth, AuthOTP}
	for _, tc := range []struct {
		flags Flags
		want  int
	}{{NewStatic(AuthOTP), http.StatusForbidden}, {NewStatic(AuthOAuth), http.StatusForbidden}, {NewStatic(MCP), http.StatusOK}} {
		w := httptest.NewRecorder()
		WhenAnyOn(tc.flags, either, refuse)(ok).ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/", nil))
		if w.Code != tc.want {
			t.Errorf("status = %d, want %d", w.Code, tc.want)
		}
	}
}

// EXC-662: end-user account self-service ships dark and Studio is told its state.
func TestAll_DeclaresAuthUser(t *testing.T) {
	if AuthUser != "authuser" {
		t.Errorf("auth user feature = %q", AuthUser)
	}
	if _, listed := Snapshot(context.Background(), NewStatic())[string(AuthUser)]; !listed {
		t.Error("authuser is not reported to Studio")
	}
	if !NewStatic(AuthUser).Enabled(context.Background(), AuthUser) {
		t.Error("authuser cannot be switched on")
	}
}
