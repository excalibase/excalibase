package sites_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

func bindingText(version cfworkers.ScriptVersion, name string) (string, bool) {
	for _, binding := range version.Bindings {
		if binding.Name == name {
			return binding.Text, binding.Type == cfworkers.BindingPlainText
		}
	}
	return "", false
}

func TestRuntimeVariablesAreBoundAsPlainTextWithThePublishableKey(t *testing.T) {
	h := newServerHarness(t, true)
	site := h.createServerSite(t, "ssr")
	vars := map[string]string{"EXCALIBASE_PUBLISHABLE_KEY": "pk_live_1", "FEATURE_X": "on"}
	if err := h.service.SetRuntimeVars(context.Background(), testProject, site.ID, vars); err != nil {
		t.Fatalf("SetRuntimeVars: %v", err)
	}
	if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 1")); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	version := h.publisher.Versions[sites.ScriptName(site)]
	for name, want := range map[string]string{"EXCALIBASE_PUBLISHABLE_KEY": "pk_live_1", "FEATURE_X": "on", "EXCALIBASE_PROJECT_ID": testProject} {
		if got, plain := bindingText(version, name); got != want || !plain {
			t.Errorf("%s = %q plain=%v, want %q", name, got, plain, want)
		}
	}
}

func TestARollbackRunsUnderTheVariablesOfTheDeployItRestores(t *testing.T) {
	h := newServerHarness(t, true)
	site := h.createServerSite(t, "ssr")
	ctx := context.Background()
	if err := h.service.SetRuntimeVars(ctx, testProject, site.ID, map[string]string{"MODE": "one"}); err != nil {
		t.Fatal(err)
	}
	first, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 'v1'"))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.service.SetRuntimeVars(ctx, testProject, site.ID, map[string]string{"MODE": "two"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.deploy(t, site, sitestest.ServerDist(t, "export default 'v2'")); err != nil {
		t.Fatal(err)
	}
	if got, _ := bindingText(h.publisher.Versions[sites.ScriptName(site)], "MODE"); got != "two" {
		t.Fatalf("MODE after the second deploy = %q", got)
	}
	if _, err := h.service.Rollback(ctx, testProject, site.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := bindingText(h.publisher.Versions[sites.ScriptName(site)], "MODE"); got != "one" {
		t.Fatalf("MODE after rollback = %q, want the snapshot of the restored deploy", got)
	}
}

func TestRuntimeVariableNamesThePlatformOwnsAreRefused(t *testing.T) {
	h := newServerHarness(t, true)
	site := h.createServerSite(t, "ssr")
	for _, name := range []string{"EXCALIBASE_URL", "EXCALIBASE_PROJECT_ID", "EXCALIBASE_DB_URL", "ASSETS", "WORKER_SELF_REFERENCE",
		"NEXT_INC_CACHE_R2_BUCKET", "NEXT_TAG_CACHE_D1", "bad name", "1ABC", ""} {
		err := h.service.SetRuntimeVars(context.Background(), testProject, site.ID, map[string]string{name: "x"})
		var input *sites.InputError
		if !errors.As(err, &input) {
			t.Errorf("%q: err = %v, want an input error", name, err)
		}
	}
}

func TestRuntimeVariablesAreBounded(t *testing.T) {
	h := newServerHarness(t, true)
	site := h.createServerSite(t, "ssr")
	var input *sites.InputError
	if err := h.service.SetRuntimeVars(context.Background(), testProject, site.ID, map[string]string{"BIG": strings.Repeat("x", 5121)}); !errors.As(err, &input) {
		t.Errorf("a value over 5 KB: err = %v", err)
	}
	many := map[string]string{}
	for i := 0; i < 65; i++ {
		many["V"+strings.Repeat("A", i%10)+string(rune('a'+i/10))+string(rune('a'+i%10))] = "x"
	}
	if err := h.service.SetRuntimeVars(context.Background(), testProject, site.ID, many); !errors.As(err, &input) {
		t.Errorf("65 variables: err = %v", err)
	}
}

func TestOnlyAServerSiteTakesRuntimeVariablesAndOnlyWhileTheFlagIsOn(t *testing.T) {
	h := newServerHarness(t, true)
	static := h.createSite(t, "plain")
	var input *sites.InputError
	if err := h.service.SetRuntimeVars(context.Background(), testProject, static.ID, map[string]string{"A": "b"}); !errors.As(err, &input) {
		t.Errorf("static site: err = %v, want an input error", err)
	}
	off := newServerHarness(t, false)
	if err := off.service.SetRuntimeVars(context.Background(), testProject, "any", map[string]string{"A": "b"}); !errors.Is(err, sites.ErrServerSitesOff) {
		t.Errorf("flag off: err = %v, want the feature's 404", err)
	}
	if _, err := off.service.RuntimeVars(context.Background(), testProject, "any"); !errors.Is(err, sites.ErrServerSitesOff) {
		t.Errorf("flag off read: err = %v", err)
	}
}

func TestRuntimeVariablesAreReadBackAndDieWithTheSite(t *testing.T) {
	h := newServerHarness(t, true)
	site := h.createServerSite(t, "ssr")
	ctx := context.Background()
	if err := h.service.SetRuntimeVars(ctx, testProject, site.ID, map[string]string{"A": "1"}); err != nil {
		t.Fatal(err)
	}
	got, err := h.service.RuntimeVars(ctx, testProject, site.ID)
	if err != nil || got["A"] != "1" || len(got) != 1 {
		t.Fatalf("RuntimeVars = %v, %v", got, err)
	}
	if _, err := h.service.RuntimeVars(ctx, "another-project", site.ID); !errors.Is(err, sites.ErrSiteNotFound) {
		t.Fatalf("another project's read: err = %v", err)
	}
	if len(h.store.Vars) != 1 {
		t.Fatalf("held %d sets", len(h.store.Vars))
	}
	if err := h.service.Delete(ctx, testProject, site.ID); err != nil {
		t.Fatal(err)
	}
	if len(h.store.Vars) != 0 {
		t.Fatal("variables outlived their site")
	}
}
