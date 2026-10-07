package edgefn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EXC-560: functions import the library and zod the way its README shows,
// by bare name, and the bundle names the copies the runtime image carries.
func TestBundle_BareRuntimeModulesResolveToTheImageCopies(t *testing.T) {
	fn := fnWithIndex(`import { z } from "zod";
import { mutation } from "@excalibase/server";
export default mutation({ args: z.object({ n: z.number() }), handler: async (_c, a) => a });`)
	out, err := fn.Bundle()
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	for _, want := range []string{`from "` + runtimeServerLib + `"`, `from "` + runtimeZod + `"`} {
		if !strings.Contains(out, want) {
			t.Errorf("bundle does not import %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, `from "zod"`) || strings.Contains(out, `from "@excalibase/server"`) {
		t.Errorf("a bare import survived:\n%s", out)
	}
}

func TestBundle_PinnedRuntimeModulesAreLeftAlone(t *testing.T) {
	fn := fnWithIndex(`import { query } from "npm:@excalibase/server@0.12.0";
export default query({ handler: async () => 1 });`)
	out, err := fn.Bundle()
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	if !strings.Contains(out, `from "npm:@excalibase/server@0.12.0"`) {
		t.Errorf("a pinned import was rewritten:\n%s", out)
	}
}

func TestBundle_OtherBareImportsAreStillRefused(t *testing.T) {
	fn := fnWithIndex(`import leftPad from "left-pad";
export default () => leftPad("x", 3);`)
	if _, err := fn.Bundle(); err == nil || !strings.Contains(err.Error(), "left-pad") {
		t.Fatalf("want the unknown module named, got %v", err)
	}
}

// The library version the bundler pins is the one the runtime image vendors
// and maps: a bump of one without the other fails here, not on a deploy.
func TestRuntimeServerLib_MatchesTheVendoredLibrary(t *testing.T) {
	root := filepath.Join("..", "..", "..", "deno-server")
	var pkg struct {
		Version string `json:"version"`
	}
	readJSON(t, filepath.Join(root, "lib", "excalibase-server", "package.json"), &pkg)
	if runtimeServerLib != "npm:@excalibase/server@"+pkg.Version {
		t.Errorf("bundler pins %s, the vendored library is %s", runtimeServerLib, pkg.Version)
	}
	var config struct {
		Imports map[string]string `json:"imports"`
	}
	readJSON(t, filepath.Join(root, "deno.json"), &config)
	for _, spec := range []string{runtimeServerLib, runtimeZod} {
		if !strings.HasPrefix(spec, "npm:zod@") && config.Imports[spec] == "" {
			t.Errorf("deno.json does not map %s", spec)
		}
	}
	if config.Imports["zod"] != runtimeZod {
		t.Errorf("deno.json maps zod to %q, the bundler pins %q", config.Imports["zod"], runtimeZod)
	}
}

func readJSON(t *testing.T, path string, into any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
}
