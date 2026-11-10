package k8s

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/sites"
)

// Nuxt, SvelteKit and Astro presets (EXC-661): the build runs with the framework's own Cloudflare output, wrangler
// bundles the Worker with our config, and the packed layout must pass the deploy path. The tools are stubs here;
// the real ones run in the integration tests.

type preset struct {
	framework, entry, assets, adapter string
	// built writes what the framework's build writes, relative to the project.
	built map[string]string
}

var presets = map[string]preset{
	"nuxt": {framework: "nuxt", entry: ".output/server/index.mjs", assets: ".output/public",
		built: map[string]string{".output/server/index.mjs": "export default {}", ".output/public/index.html": "<html>", ".output/public/_nuxt/a.js": "1"}},
	"sveltekit": {framework: "sveltekit", entry: ".svelte-kit/cloudflare/_worker.js", assets: ".svelte-kit/cloudflare", adapter: "@sveltejs/adapter-cloudflare",
		built: map[string]string{".svelte-kit/cloudflare/_worker.js": "export default {}", ".svelte-kit/cloudflare/index.html": "<html>", ".svelte-kit/cloudflare/_app/a.js": "1"}},
	"astro": {framework: "astro", entry: "dist/_worker.js/index.js", assets: "dist", adapter: "@astrojs/cloudflare",
		built: map[string]string{"dist/_worker.js/index.js": "export default {}", "dist/_worker.js/chunk.mjs": "1", "dist/index.html": "<html>", "dist/_astro/a.js": "1"}},
}

// presetProject lays out a repository whose build writes the preset's output; wrangler is a stub.
func presetProject(t *testing.T, work string, p preset, declared bool, skipOutput bool) string {
	t.Helper()
	project := filepath.Join(work, "src")
	dependencies := `"` + p.framework + `":"1.0.0"`
	if declared && p.adapter != "" {
		dependencies += `,"` + p.adapter + `":"1.0.0"`
	}
	write(t, filepath.Join(project, "package.json"), `{"scripts":{"build":"true"},"devDependencies":{`+dependencies+`}}`)
	var commands []string
	commands = append(commands, `echo "$NITRO_PRESET" > "$WORK_DIR/nitro-preset"`, `echo build >> "$WORK_DIR/calls"`)
	if !skipOutput {
		for path, body := range p.built {
			commands = append(commands, "mkdir -p "+filepath.Dir(path), "echo '"+body+"' > "+path)
		}
	}
	write(t, filepath.Join(work, "build-command.sh"), strings.Join(commands, "\n")+"\n")
	// Like the real wrangler: a _worker.js left in the assets folder is refused unless .assetsignore names it.
	write(t, filepath.Join(project, "node_modules/.bin/wrangler"), "#!/bin/bash\necho \"wrangler $*\" >> \"$WORK_DIR/calls\"\n"+
		"for dir in .svelte-kit/cloudflare dist; do if [[ -e $dir/_worker.js ]] && ! grep -qx _worker.js $dir/.assetsignore 2>/dev/null; then echo 'Uploading a Pages _worker.js directory as an asset' >&2; exit 1; fi; done\n"+
		"while [[ $# -gt 0 ]]; do [[ $1 == --outdir ]] && out=$2; [[ $1 == --config ]] && config=$2 && cat \"$2\" > \"$WORK_DIR/bundled-with\"; shift; done\n"+
		"main=$(sed -n 's/.*\"main\": \"\\(.*\\)\",/\\1/p' \"$config\")\nmkdir -p \"$out\"\necho 'export default {}' > \"$out/$(basename \"$main\")\"\n")
	write(t, filepath.Join(project, "node_modules/wrangler/package.json"), `{"name":"wrangler","type":"module","exports":"./index.js"}`)
	write(t, filepath.Join(project, "node_modules/wrangler/index.js"),
		`import { readFileSync } from "node:fs"; export const unstable_readConfig = (args) => JSON.parse(readFileSync(args.config, "utf8"));`)
	return project
}

func presetBuild(t *testing.T, work, scripts, framework string) (int, string) {
	t.Helper()
	return runScript(t, work, scripts, "build.sh", "FRAMEWORK="+framework, "ROOT_DIR=", "INSTALL_COMMAND=true", "BUILD_COMMAND=bash "+filepath.Join(work, "build-command.sh"))
}

func TestWorkerPresets_BuildBundleAndPackWhatTheDeployPathAccepts(t *testing.T) {
	for name, p := range presets {
		t.Run(name, func(t *testing.T) {
			work, scripts := t.TempDir(), scriptDir(t)
			presetProject(t, work, p, true, false)
			if code, out := presetBuild(t, work, scripts, p.framework); code != 0 {
				t.Fatalf("build exit %d:\n%s", code, out)
			}
			used := bundledWith(t, work)
			if !strings.Contains(used, `"main": "`+p.entry+`"`) || !strings.Contains(used, `"directory": "`+p.assets+`"`) ||
				!strings.Contains(used, `"binding": "ASSETS"`) || !strings.Contains(used, `"compatibility_date": "2025-09-01"`) {
				t.Errorf("bundled with the wrong config:\n%s", used)
			}
			if code, out := runScript(t, work, scripts, "collect.sh", "FRAMEWORK="+p.framework, "ROOT_DIR=", "OUTPUT_DIR="); code != 0 {
				t.Fatalf("collect exit %d:\n%s", code, out)
			}
			archive, err := os.Open(filepath.Join(work, "collect", "site.tar.gz"))
			if err != nil {
				t.Fatal(err)
			}
			defer archive.Close()
			bundle, err := sites.ReadServerArchive(archive, sites.DefaultLimits, t.TempDir())
			if err != nil {
				t.Fatalf("the deploy path refused the packed output: %v", err)
			}
			defer bundle.Close()
			if bundle.Server == nil || bundle.Server.Framework != p.framework || bundle.Server.CompatibilityDate != "2025-09-01" {
				t.Errorf("server config %+v", bundle.Server)
			}
			// Server code must never be served as a public file.
			for _, file := range bundle.Files {
				if strings.Contains(file.Path, "_worker.js") || strings.HasSuffix(file.Path, "index.mjs") || strings.Contains(file.Path, ".assetsignore") {
					t.Errorf("the worker is public: %s", file.Path)
				}
			}
			if len(bundle.Modules) != 1 || len(bundle.Files) < 2 {
				t.Errorf("modules %d, assets %d", len(bundle.Modules), len(bundle.Files))
			}
		})
	}
}

func TestWorkerPresets_NuxtBuildsForTheCloudflareModulePreset(t *testing.T) {
	work, scripts := t.TempDir(), scriptDir(t)
	presetProject(t, work, presets["nuxt"], true, false)
	if code, out := presetBuild(t, work, scripts, "nuxt"); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got, _ := os.ReadFile(filepath.Join(work, "nitro-preset")); strings.TrimSpace(string(got)) != "cloudflare_module" {
		t.Errorf("NITRO_PRESET = %q", got)
	}
}

// SvelteKit and Astro pick their output from an adapter in the repository's own config; the build does not edit it.
func TestWorkerPresets_ARepositoryWithoutTheCloudflareAdapterIsRefusedByName(t *testing.T) {
	for _, name := range []string{"sveltekit", "astro"} {
		p := presets[name]
		work, scripts := t.TempDir(), scriptDir(t)
		presetProject(t, work, p, false, false)
		code, out := presetBuild(t, work, scripts, p.framework)
		if code != exitFramework || !strings.Contains(out, p.adapter) || !strings.Contains(out, "app container") {
			t.Errorf("%s: exit %d, want %d naming %s:\n%s", name, code, exitFramework, p.adapter, out)
		}
		if ran := calls(t, work); len(ran) != 0 {
			t.Errorf("%s: a build ran before the refusal: %v", name, ran)
		}
	}
}

func TestWorkerPresets_ABuildThatWroteNoWorkerIsRefusedWithAPointer(t *testing.T) {
	for name, p := range presets {
		work, scripts := t.TempDir(), scriptDir(t)
		presetProject(t, work, p, true, true)
		code, out := presetBuild(t, work, scripts, p.framework)
		if code != exitFramework || !strings.Contains(out, p.entry) || !strings.Contains(out, "app container") {
			t.Errorf("%s: exit %d, want %d naming %s:\n%s", name, code, exitFramework, p.entry, out)
		}
	}
}

func TestWorkerPresets_RepositoryBindingsAreRefusedLikeNextjs(t *testing.T) {
	work, scripts := t.TempDir(), scriptDir(t)
	project := presetProject(t, work, presets["nuxt"], true, false)
	write(t, filepath.Join(project, "wrangler.jsonc"), `{"r2_buckets":[{"binding":"B","bucket_name":"x"}]}`)
	code, out := presetBuild(t, work, scripts, "nuxt")
	if code != exitFramework || !strings.Contains(out, "an R2 bucket") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}
