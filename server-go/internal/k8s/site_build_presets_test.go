package k8s

import (
	"os"
	"path/filepath"
	"slices"
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
			assertBundledWith(t, work, p)
			bundle := packPreset(t, work, scripts, p.framework)
			assertPackedLayout(t, bundle, p)
		})
	}
}

func assertBundledWith(t *testing.T, work string, p preset) {
	t.Helper()
	used := bundledWith(t, work)
	if !strings.Contains(used, `"main": "`+p.entry+`"`) || !strings.Contains(used, `"directory": "`+p.assets+`"`) ||
		!strings.Contains(used, `"binding": "ASSETS"`) || !strings.Contains(used, `"compatibility_date": "2025-09-01"`) {
		t.Errorf("bundled with the wrong config:\n%s", used)
	}
}

func packPreset(t *testing.T, work, scripts, framework string) *sites.Bundle {
	t.Helper()
	if code, out := runScript(t, work, scripts, "collect.sh", "FRAMEWORK="+framework, "ROOT_DIR=", "OUTPUT_DIR="); code != 0 {
		t.Fatalf("collect exit %d:\n%s", code, out)
	}
	archive, err := os.Open(filepath.Join(work, "collect", "site.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = archive.Close() })
	bundle, err := sites.ReadServerArchive(archive, sites.DefaultLimits, t.TempDir())
	if err != nil {
		t.Fatalf("the deploy path refused the packed output: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	return bundle
}

func assertPackedLayout(t *testing.T, bundle *sites.Bundle, p preset) {
	t.Helper()
	if bundle.Server == nil || bundle.Server.Framework != p.framework || bundle.Server.CompatibilityDate != "2025-09-01" {
		t.Errorf("server config %+v", bundle.Server)
	}
	// Server code must never be served as a public file.
	for _, file := range bundle.Files {
		if workerIsPublic(file.Path) {
			t.Errorf("the worker is public: %s", file.Path)
		}
	}
	if len(bundle.Modules) != 1 || len(bundle.Files) < 2 {
		t.Errorf("modules %d, assets %d", len(bundle.Modules), len(bundle.Files))
	}
}

func workerIsPublic(path string) bool {
	return strings.Contains(path, "_worker.js") || strings.HasSuffix(path, "index.mjs") || strings.Contains(path, ".assetsignore")
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

// wrangler publishes nothing a repository's own .assetsignore names, besides the worker.
func TestWorkerPresets_TheRepositorysAssetsignoreKeepsItsFilesOutOfThePublicAssets(t *testing.T) {
	p := presets["sveltekit"]
	built := map[string]string{}
	for path, body := range p.built {
		built[path] = body
	}
	built[".svelte-kit/cloudflare/_headers"] = "/*"
	built[".svelte-kit/cloudflare/_routes.json"] = "{}"
	built[".svelte-kit/cloudflare/keep/me.txt"] = "kept"
	built[".svelte-kit/cloudflare/deep/notes.private"] = "no"
	built[".svelte-kit/cloudflare/private-dir/a.txt"] = "no"
	p.built = built
	work, scripts := t.TempDir(), scriptDir(t)
	project := presetProject(t, work, p, true, false)
	// No trailing newline, a comment, a blank line, CRLF, a directory, a wildcard and a pattern that tries to leave the folder.
	write(t, filepath.Join(project, ".svelte-kit/cloudflare/.assetsignore"), "# private\r\n\r\n_headers\r\n/_routes.json\r\n*.private\r\nprivate-dir/\r\n../../../etc/passwd\r\nlast-line-no-newline")
	// The build command rewrites the assets folder, so the repository's file is laid down by it.
	command, _ := os.ReadFile(filepath.Join(work, "build-command.sh"))
	ignore, _ := os.ReadFile(filepath.Join(project, ".svelte-kit/cloudflare/.assetsignore"))
	_ = os.Remove(filepath.Join(project, ".svelte-kit/cloudflare/.assetsignore"))
	write(t, filepath.Join(work, "build-command.sh"), string(command)+"printf '%s' '"+strings.ReplaceAll(string(ignore), "'", "")+"' > .svelte-kit/cloudflare/.assetsignore\n")
	if code, out := presetBuild(t, work, scripts, p.framework); code != 0 {
		t.Fatalf("build exit %d:\n%s", code, out)
	}
	bundle := packPreset(t, work, scripts, p.framework)
	var got []string
	for _, file := range bundle.Files {
		got = append(got, file.Path)
	}
	for _, public := range got {
		for _, hidden := range []string{"_headers", "_routes.json", ".private", "private-dir", "_worker.js", ".assetsignore"} {
			if strings.Contains(public, hidden) {
				t.Errorf("%s is public although the repository's .assetsignore names it (%v)", public, got)
			}
		}
	}
	if !slices.Contains(got, "/keep/me.txt") && !slices.Contains(got, "keep/me.txt") {
		t.Errorf("a file nobody ignored was dropped: %v", got)
	}
}

const yarnEnvRecorder = "#!/bin/bash\necho \"scripts=${YARN_ENABLE_SCRIPTS:-} ignore=${YARN_IGNORE_SCRIPTS:-}\" >> \"$WORK_DIR/yarn-env\"\n"

func assertYarnScriptsOff(t *testing.T, work string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(work, "yarn-env"))
	if err != nil {
		t.Fatalf("yarn was never asked to install the tools: %v", err)
	}
	// Yarn 2+ reads YARN_ENABLE_SCRIPTS, Yarn 1 (what corepack gives a repository without packageManager) only YARN_IGNORE_SCRIPTS.
	if !strings.Contains(string(got), "scripts=0 ignore=true") {
		t.Errorf("install scripts may run: %q", got)
	}
}

func TestWorkerPresets_ToolInstallWithYarnRunsNoInstallScripts(t *testing.T) {
	p := presets["astro"]
	work, scripts := t.TempDir(), scriptDir(t)
	project := presetProject(t, work, p, true, false)
	write(t, filepath.Join(project, "yarn.lock"), "")
	wrangler, _ := os.ReadFile(filepath.Join(project, "node_modules/.bin/wrangler"))
	write(t, filepath.Join(work, "tool-wrangler"), string(wrangler))
	if err := os.Remove(filepath.Join(project, "node_modules/.bin/wrangler")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(work, "bin", "corepack"), yarnEnvRecorder+"mkdir -p node_modules/.bin\ncp \"$WORK_DIR/tool-wrangler\" node_modules/.bin/wrangler\n")
	if code, out := presetBuild(t, work, scripts, p.framework); code != 0 {
		t.Fatalf("build exit %d:\n%s", code, out)
	}
	assertYarnScriptsOff(t, work)
}

func TestNextjsPreset_ToolInstallWithYarnRunsNoInstallScripts(t *testing.T) {
	work, scripts := t.TempDir(), scriptDir(t)
	tooless(t, work, "16.4.1", "yarn.lock")
	stub, _ := os.ReadFile(filepath.Join(work, "bin", "corepack"))
	write(t, filepath.Join(work, "bin", "corepack"), yarnEnvRecorder+strings.TrimPrefix(string(stub), "#!/bin/bash\n"))
	build(t, work, scripts, "")
	assertYarnScriptsOff(t, work)
}

func TestWorkerPresets_AnAssetsignorePatternIsKeptOrSkippedByItsLeadingParentOnly(t *testing.T) {
	p := presets["sveltekit"]
	built := map[string]string{}
	for path, body := range p.built {
		built[path] = body
	}
	built[".svelte-kit/cloudflare/notes..bak"] = "no"
	built[".svelte-kit/cloudflare/keep.txt"] = "kept"
	p.built = built
	work, scripts := t.TempDir(), scriptDir(t)
	presetProject(t, work, p, true, false)
	command, _ := os.ReadFile(filepath.Join(work, "build-command.sh"))
	write(t, filepath.Join(work, "build-command.sh"), string(command)+"printf 'notes..bak\\n../keep.txt\\n' > .svelte-kit/cloudflare/.assetsignore\n")
	if code, out := presetBuild(t, work, scripts, p.framework); code != 0 {
		t.Fatalf("build exit %d:\n%s", code, out)
	}
	bundle := packPreset(t, work, scripts, p.framework)
	var got []string
	for _, file := range bundle.Files {
		got = append(got, file.Path)
	}
	for _, public := range got {
		if strings.Contains(public, "notes..bak") {
			t.Errorf("notes..bak is public although .assetsignore names it: %v", got)
		}
	}
	if !slices.Contains(got, "/keep.txt") && !slices.Contains(got, "keep.txt") {
		t.Errorf("a pattern that leaves the folder must be skipped, not applied: %v", got)
	}
}
