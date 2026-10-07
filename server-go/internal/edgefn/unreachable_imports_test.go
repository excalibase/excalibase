package edgefn

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// EXC-560: an import the runtime does not carry is fetched when the worker
// starts; with the project's egress closed that fetch hangs until "worker init
// timeout". The deploy names such imports up front instead.
func TestUnreachableImports(t *testing.T) {
	code := `import { z } from "npm:zod@^3.22.0";
import { query } from "npm:@excalibase/server@0.13.0";
import { query as q2 } from "npm:@excalibase/server@0.10.0";
import chunk from "npm:lodash.chunk@4.2.0";
import "npm:side-effect@1";
export { thing } from "jsr:@std/path@1";
const later = await import("https://esm.sh/preact@10");
import { readFile } from "node:fs/promises";
import { b } from "npm:lodash.chunk@4.2.0";
`
	cases := []struct {
		name  string
		hosts []string
		want  []string
	}{
		{"egress closed", nil, []string{
			"https://esm.sh/preact@10 (esm.sh)",
			"jsr:@std/path@1 (jsr.io)",
			"npm:lodash.chunk@4.2.0 (registry.npmjs.org)",
			"npm:side-effect@1 (registry.npmjs.org)",
		}},
		{"registry allowed", []string{"registry.npmjs.org:443"}, []string{
			"https://esm.sh/preact@10 (esm.sh)",
			"jsr:@std/path@1 (jsr.io)",
		}},
		{"wildcard and exact cover the rest", []string{"registry.npmjs.org:443", "*.jsr.io:443", "jsr.io:443", "esm.sh:443"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UnreachableImports(code, tc.hosts)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUnreachableImports_CarriedModulesNeverNeedEgress(t *testing.T) {
	code := ""
	for spec := range carriedImports {
		code += `import x from "` + spec + `";` + "\n"
	}
	for _, pinned := range runtimeModules {
		code += `import y from "` + pinned + `";` + "\n"
	}
	if got := UnreachableImports(code, nil); len(got) != 0 {
		t.Fatalf("carried imports reported unreachable: %q", got)
	}
}

// The carried set is what the image's deno.json resolves without a network:
// its npm: keys, and the npm: specs its bare names map to.
func TestCarriedImports_MatchTheRuntimeImportMap(t *testing.T) {
	var config struct {
		Imports map[string]string `json:"imports"`
	}
	readJSON(t, filepath.Join("..", "..", "..", "deno-server", "deno.json"), &config)
	want := map[string]bool{}
	for key, target := range config.Imports {
		if strings.HasPrefix(key, "npm:") {
			want[key] = true
		}
		if strings.HasPrefix(target, "npm:") {
			want[target] = true
		}
	}
	if !reflect.DeepEqual(carriedImports, want) {
		t.Fatalf("carriedImports = %v, deno.json carries %v", carriedImports, want)
	}
}

// Modules load over 443: an entry for another port does not reach them, and
// a specifier with no readable host is reported as such.
func TestUnreachableImports_OtherPortsAndBadURLs(t *testing.T) {
	code := `import a from "https://cdn.example.com/a.js"; import b from "https://%zz/b.js";`
	got := UnreachableImports(code, []string{"cdn.example.com:8443"})
	want := []string{"https://%zz/b.js ()", "https://cdn.example.com/a.js (cdn.example.com)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestUnreachableImports_WildcardMatchesSubdomainsOnly(t *testing.T) {
	code := `import a from "https://cdn.example.com/a.js"; import b from "https://example.com/b.js";`
	got := UnreachableImports(code, []string{"*.example.com:443"})
	if want := []string{"https://example.com/b.js (example.com)"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}
