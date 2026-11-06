package k8s

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/sites"
)

// These run the real build scripts under bash against a temp /work. The framework tools are stubs that
// write the files the real ones write (the resolved OpenNext config, Next's functions manifest, the
// bundle), so the preset's steps, refusals and packed layout are pinned without a cluster. The real tools
// run in TestNextjsPreset_BuildsAPlainRepoWithRealTools (integration).

const resolvedStatic = `export default { default: { override: { incrementalCache: () => ({ name: "cf-static-assets-incremental-cache" }), tagCache: "dummy", queue: "dummy" } }, middleware: { override: { incrementalCache: () => ({ name: "cf-static-assets-incremental-cache" }) } } };`

func scriptDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"build.sh", "collect.sh"} {
		body, err := SiteBuildScript(name)
		if err != nil {
			t.Fatalf("script %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// runScript runs one script with the work and scripts folders redirected; it answers the exit code and output.
// A "bin" folder under work, when present, comes first on PATH (stubbed npm and corepack).
func runScript(t *testing.T, work, scripts, name string, env ...string) (int, string) {
	t.Helper()
	command := exec.Command("bash", filepath.Join(scripts, name))
	path := filepath.Join(work, "bin") + ":" + os.Getenv("PATH")
	command.Env = append([]string{"PATH=" + path, "WORK_DIR=" + work, "SCRIPTS_DIR=" + scripts, "HOME=" + filepath.Join(work, "home"), "HOLD_SECONDS=0"}, env...)
	var out bytes.Buffer
	command.Stdout, command.Stderr = &out, &out
	err := command.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, out.String()
	case errors.As(err, &exit):
		return exit.ExitCode(), out.String()
	}
	t.Fatalf("run %s: %v", name, err)
	return -1, ""
}

func build(t *testing.T, work, scripts string, env ...string) (int, string) {
	t.Helper()
	return runScript(t, work, scripts, "build.sh", append([]string{"FRAMEWORK=nextjs", "ROOT_DIR=", "INSTALL_COMMAND=true"}, env...)...)
}

func calls(t *testing.T, work string) []string {
	t.Helper()
	body, _ := os.ReadFile(filepath.Join(work, "calls"))
	if len(body) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(body)), "\n")
}

// nextProject lays out a repository (under root) whose tools are stubs. opennextFails and the like name the
// stub that exits 1.
func nextProject(t *testing.T, work, root string, failing ...string) string {
	t.Helper()
	project := filepath.Join(work, "src", root)
	write(t, filepath.Join(project, "package.json"), `{"scripts":{"build":"next build"}}`)
	fails := func(name string) string {
		for _, failed := range failing {
			if failed == name {
				return "exit 1\n"
			}
		}
		return ""
	}
	tools := filepath.Join(project, "node_modules/.bin")
	write(t, filepath.Join(tools, "opennextjs-cloudflare"), "#!/bin/bash\necho \"opennext $*\" >> \"$WORK_DIR/calls\"\n"+
		"if [[ $1 == populateCache ]]; then "+orTrue(strings.TrimSpace(fails("populate")))+"; exit 0; fi\n"+fails("opennext")+
		"echo 'WARN Node.js middleware support is experimental (printed by a page, not a fact)'\n"+
		"mkdir -p .open-next/assets .open-next/server-functions/default .next/server\necho '<html>' > .open-next/assets/index.html\n"+
		"cp \"$WORK_DIR/resolved.mjs\" .open-next/server-functions/default/open-next.config.mjs\n"+
		"cp \"$WORK_DIR/manifest.json\" .next/server/functions-config-manifest.json\n")
	write(t, filepath.Join(tools, "wrangler"), "#!/bin/bash\necho \"wrangler $*\" >> \"$WORK_DIR/calls\"\n"+fails("wrangler")+
		"while [[ $# -gt 0 ]]; do [[ $1 == --outdir ]] && out=$2; [[ $1 == --config ]] && cat \"$2\" > \"$WORK_DIR/bundled-with\"; shift; done\n"+
		"mkdir -p \"$out\"\necho 'export default {}' > \"$out/worker.js\"\necho '{}' > \"$out/worker.js.map\"\necho readme > \"$out/README.md\"\n")
	// The helper reads the repository's wrangler file through wrangler's own reader; the stub reads plain JSON.
	write(t, filepath.Join(project, "node_modules/wrangler/package.json"), `{"name":"wrangler","type":"module","exports":"./index.js"}`)
	write(t, filepath.Join(project, "node_modules/wrangler/index.js"),
		`import { readFileSync } from "node:fs"; export const unstable_readConfig = (args) => JSON.parse(readFileSync(args.config, "utf8"));`)
	write(t, filepath.Join(work, "resolved.mjs"), resolvedStatic)
	write(t, filepath.Join(work, "manifest.json"), `{"version":1,"functions":{}}`)
	return project
}

func orTrue(command string) string {
	if command == "" {
		return "true"
	}
	return command
}

func bundledWith(t *testing.T, work string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(work, "bundled-with"))
	if err != nil {
		t.Fatalf("wrangler did not bundle: %v", err)
	}
	return string(body)
}

func TestNextjsPreset_BuildsPopulatesAndBundlesInOrder(t *testing.T) {
	work, scripts := t.TempDir(), scriptDir(t)
	project := nextProject(t, work, "")
	code, out := build(t, work, scripts)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	got := calls(t, work)
	want := []string{"opennext build", "opennext populateCache local", "wrangler deploy --dry-run --minify --outdir " + filepath.Join(work, "bundle") + " --config wrangler.excalibase.jsonc"}
	if len(got) != len(want) {
		t.Fatalf("calls %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %q, want %q", i, got[i], want[i])
		}
	}
	config, _ := os.ReadFile(filepath.Join(project, "open-next.config.ts"))
	if !strings.Contains(string(config), "incrementalCache: staticAssetsIncrementalCache") || !strings.Contains(string(config), "enableCacheInterception: true") {
		t.Errorf("the generated OpenNext config does not use the static-assets cache:\n%s", config)
	}
}

// The repository's wrangler date is not the date the Worker runs on: a 2026-10-01 bundle fails at startup on 2025-09-01.
func TestNextjsPreset_BundlesWithOurDateNotTheRepositorys(t *testing.T) {
	work, scripts := t.TempDir(), scriptDir(t)
	project := nextProject(t, work, "")
	write(t, filepath.Join(project, "wrangler.jsonc"), `{"name":"mine","compatibility_date":"2026-10-01","compatibility_flags":["nodejs_compat","nodejs_compat_populate_process_env"]}`)
	if code, out := build(t, work, scripts); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	used := bundledWith(t, work)
	if !strings.Contains(used, `"compatibility_date": "2025-09-01"`) || strings.Contains(used, "2026-10-01") || strings.Contains(used, "populate_process_env") {
		t.Errorf("the bundle used the repository's runtime settings:\n%s", used)
	}
	var site struct {
		Date  string   `json:"compatibilityDate"`
		Flags []string `json:"compatibilityFlags"`
	}
	body, _ := os.ReadFile(filepath.Join(work, "site.json"))
	if err := json.Unmarshal(body, &site); err != nil || site.Date != "2025-09-01" || strings.Join(site.Flags, ",") != "nodejs_compat" {
		t.Errorf("site.json %s (%v): it must say the date and flags the bundle was built for", body, err)
	}
}

func TestNextjsPreset_RefusesRepositoryBindingsByName(t *testing.T) {
	cases := map[string]struct{ config, named string }{
		"r2":              {`{"r2_buckets":[{"binding":"B","bucket_name":"x"}]}`, "an R2 bucket"},
		"service":         {`{"services":[{"binding":"S","service":"other"}]}`, "a service binding"},
		"durable object":  {`{"durable_objects":{"bindings":[{"name":"D","class_name":"C"}]}}`, "a Durable Object"},
		"unsafe":          {`{"unsafe":{"bindings":[{"name":"E","type":"unsafe_eval"}]}}`, "an unsafe binding"},
		"queue consumers": {`{"queues":{"consumers":[{"queue":"q"}]}}`, "a queue"},
	}
	for name, c := range cases {
		work, scripts := t.TempDir(), scriptDir(t)
		project := nextProject(t, work, "")
		write(t, filepath.Join(project, "wrangler.jsonc"), c.config)
		code, out := build(t, work, scripts)
		if code != exitFramework || !strings.Contains(out, c.named) || !strings.Contains(out, "app container") {
			t.Errorf("%s: exit %d, want %d naming %q:\n%s", name, code, exitFramework, c.named, out)
		}
		if ran := calls(t, work); len(ran) != 0 {
			t.Errorf("%s: a tool ran before the refusal: %v", name, ran)
		}
	}
}

func TestNextjsPreset_SaysWhichRepositorySettingsItIgnores(t *testing.T) {
	work, scripts := t.TempDir(), scriptDir(t)
	project := nextProject(t, work, "")
	write(t, filepath.Join(project, "wrangler.jsonc"), `{"vars":{"A":"1"},"routes":["x.example.com/*"],"account_id":"abc"}`)
	code, out := build(t, work, scripts)
	if code != 0 || !strings.Contains(out, "vars, routes, account_id") || !strings.Contains(out, "ignored") {
		t.Fatalf("exit %d, want a pass that names the ignored settings:\n%s", code, out)
	}
}

func TestNextjsPreset_RefusesNodeMiddlewareFromNextsOwnManifest(t *testing.T) {
	work, scripts := t.TempDir(), scriptDir(t)
	nextProject(t, work, "")
	write(t, filepath.Join(work, "manifest.json"), `{"version":1,"functions":{"/_middleware":{"runtime":"nodejs"}}}`)
	code, out := build(t, work, scripts)
	if code != exitFramework || !strings.Contains(out, "Node.js middleware") || !strings.Contains(out, "app container") {
		t.Fatalf("exit %d, want %d naming Node.js middleware and the app container:\n%s", code, exitFramework, out)
	}
	if ran := calls(t, work); len(ran) != 1 {
		t.Errorf("the build went on past the refusal: %v", ran)
	}
}

// A page that logs the words, or a comment in the config, is not Node middleware.
func TestNextjsPreset_APageLoggingNodeMiddlewareIsNotRefused(t *testing.T) {
	work, scripts := t.TempDir(), scriptDir(t)
	project := nextProject(t, work, "")
	write(t, filepath.Join(project, "open-next.config.ts"), "// durable, r2-incremental-cache: words in a comment\nexport default {};")
	if code, out := build(t, work, scripts); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestNextjsPreset_RefusesCacheOverridesFromTheResolvedConfig(t *testing.T) {
	cases := map[string]struct{ resolved, named string }{
		"r2 incremental cache": {`export default { default: { override: { incrementalCache: () => ({ name: "cf-r2-incremental-cache" }) } } };`, "cf-r2-incremental-cache"},
		"kv incremental cache": {`export default { default: { override: { incrementalCache: () => ({ name: "cf-kv-incremental-cache" }) } } };`, "cf-kv-incremental-cache"},
		"d1 tag cache":         {`export default { default: { override: { tagCache: () => ({ name: "d1-next-mode-tag-cache" }) } } };`, "d1-next-mode-tag-cache"},
		"durable queue":        {`export default { default: { override: { queue: () => ({ name: "durable-queue" }) } } };`, "durable-queue"},
		"sharded tag cache":    {`export default { default: { override: { tagCache: () => ({ name: "do-sharded-tag-cache" }) } } };`, "do-sharded-tag-cache"},
		"only the middleware":  {`export default { default: { override: {} }, middleware: { override: { incrementalCache: () => ({ name: "cf-r2-incremental-cache" }) } } };`, "cf-r2-incremental-cache"},
	}
	for name, c := range cases {
		work, scripts := t.TempDir(), scriptDir(t)
		project := nextProject(t, work, "")
		// Re-exported from a helper: no text in this file names the override, only the resolved config does.
		write(t, filepath.Join(project, "open-next.config.ts"), `export { default } from "./cachecfg";`)
		write(t, filepath.Join(work, "resolved.mjs"), c.resolved)
		code, out := build(t, work, scripts)
		if code != exitFramework || !strings.Contains(out, c.named) || !strings.Contains(out, "app container") {
			t.Errorf("%s: exit %d, want %d naming %s:\n%s", name, code, exitFramework, c.named, out)
		}
		for _, ran := range calls(t, work) {
			if strings.Contains(ran, "populateCache") || strings.Contains(ran, "wrangler") {
				t.Errorf("%s: %q ran after the refusal", name, ran)
			}
		}
	}
}

func TestNextjsPreset_AToolFailureIsABuildFailureNotASuccess(t *testing.T) {
	for _, failing := range []string{"opennext", "populate", "wrangler"} {
		work, scripts := t.TempDir(), scriptDir(t)
		nextProject(t, work, "", failing)
		if code, out := build(t, work, scripts); code != exitBuild {
			t.Errorf("%s fails: exit %d, want %d:\n%s", failing, code, exitBuild, out)
		}
	}
	work, scripts := t.TempDir(), scriptDir(t)
	nextProject(t, work, "")
	code, _ := build(t, work, scripts, "BUILD_COMMAND=exit 7")
	if code != exitBuild {
		t.Errorf("the user's build command fails: exit %d, want %d", code, exitBuild)
	}
	if ran := calls(t, work); len(ran) != 0 {
		t.Errorf("OpenNext ran after the user's build command failed: %v", ran)
	}
}

func TestNextjsPreset_UsersBuildCommandRunsAndOpenNextSkipsItsOwn(t *testing.T) {
	work, scripts := t.TempDir(), scriptDir(t)
	nextProject(t, work, "")
	code, out := build(t, work, scripts, "BUILD_COMMAND=echo mine >> $WORK_DIR/calls")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := calls(t, work); len(got) < 2 || got[0] != "mine" || got[1] != "opennext build --skipNextBuild" {
		t.Errorf("calls:\n%v", got)
	}
}

// A repository that declares neither tool still builds: the OpenNext release that supports its Next.js is added to the checkout.
func TestNextjsPreset_AddsTheToolsOfARepositoryThatDoesNotDeclareThem(t *testing.T) {
	pairs := map[string]string{"16.3.8": "1.20.11", "16.4.0": "1.20.11", "16.4.1": "1.20.12", "16.5.2": "1.20.12", "15.5.27": "1.20.11", "15.5.28": "1.20.12", "15.6.0": "1.20.12"}
	for next, opennext := range pairs {
		for lockfile, manager := range map[string]string{"": "npm", "pnpm-lock.yaml": "pnpm", "yarn.lock": "yarn"} {
			work, scripts := t.TempDir(), scriptDir(t)
			project := tooless(t, work, next, lockfile)
			code, out := build(t, work, scripts)
			adds, _ := os.ReadFile(filepath.Join(work, "adds"))
			want := "@opennextjs/cloudflare@" + opennext + " wrangler@4.150.0"
			if code != 0 || !strings.Contains(string(adds), want) || !strings.Contains(string(adds), manager) {
				t.Errorf("next %s with %s: exit %d, adds %q, want %q:\n%s", next, manager, code, adds, want, out)
			}
			_ = project
		}
	}
}

func TestNextjsPreset_RefusesANextVersionNoOpenNextSupports(t *testing.T) {
	for _, next := range []string{"14.2.0", "15.5.26", "16.3.7", "16.0.0"} {
		work, scripts := t.TempDir(), scriptDir(t)
		tooless(t, work, next, "")
		code, out := build(t, work, scripts)
		if code != exitFramework || !strings.Contains(out, "Next.js version") || !strings.Contains(out, "app container") {
			t.Errorf("next %s: exit %d, want %d naming the version:\n%s", next, code, exitFramework, out)
		}
		if adds, _ := os.ReadFile(filepath.Join(work, "adds")); len(adds) != 0 {
			t.Errorf("next %s: the tools were added anyway: %s", next, adds)
		}
	}
}

// tooless is a project with Next.js at version next that declares neither tool; the package manager stub
// records the add and then provides the tools, as installing them would.
func tooless(t *testing.T, work, next, lockfile string) string {
	t.Helper()
	project := nextProject(t, work, "")
	if err := os.RemoveAll(filepath.Join(project, "node_modules/.bin")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(project, "node_modules/next/package.json"), `{"name":"next","version":"`+next+`"}`)
	if lockfile != "" {
		write(t, filepath.Join(project, lockfile), "")
	}
	stub := "#!/bin/bash\necho \"$(basename $0) $*\" >> \"$WORK_DIR/adds\"\n[[ $1 == pnpm || $1 == yarn ]] && shift\n[[ $1 == add || $1 == install ]] || exit 0\n" +
		"mkdir -p node_modules/.bin\nfor tool in opennextjs-cloudflare wrangler; do cp \"$WORK_DIR/tool-$tool\" node_modules/.bin/$tool; done\n"
	write(t, filepath.Join(work, "bin", "npm"), stub)
	write(t, filepath.Join(work, "bin", "corepack"), stub)
	for _, tool := range []string{"opennextjs-cloudflare", "wrangler"} {
		write(t, filepath.Join(work, "tool-"+tool), "#!/bin/bash\necho \""+tool+" $*\" >> \"$WORK_DIR/calls\"\n"+
			"while [[ $# -gt 0 ]]; do [[ $1 == --outdir ]] && out=$2; shift; done\n[[ -n ${out:-} ]] && mkdir -p \"$out\" && echo x > \"$out/worker.js\"\n"+
			"[[ "+tool+" == opennextjs-cloudflare ]] && mkdir -p .open-next/assets .open-next/server-functions/default .next/server && cp \"$WORK_DIR/resolved.mjs\" .open-next/server-functions/default/open-next.config.mjs && cp \"$WORK_DIR/manifest.json\" .next/server/functions-config-manifest.json\nexit 0\n")
	}
	return project
}

func TestNextjsPreset_FailedToolInstallIsABuildFailure(t *testing.T) {
	work, scripts := t.TempDir(), scriptDir(t)
	tooless(t, work, "16.4.1", "")
	write(t, filepath.Join(work, "bin", "npm"), "#!/bin/bash\nexit 1\n")
	if code, out := build(t, work, scripts); code != exitBuild {
		t.Fatalf("exit %d, want %d:\n%s", code, exitBuild, out)
	}
}

func packedServerArchive(t *testing.T, root string) *sites.Bundle {
	t.Helper()
	work, scripts := t.TempDir(), scriptDir(t)
	nextProject(t, work, root)
	if code, out := build(t, work, scripts, "ROOT_DIR="+root); code != 0 {
		t.Fatalf("build exit %d:\n%s", code, out)
	}
	if code, out := runScript(t, work, scripts, "collect.sh", "FRAMEWORK=nextjs", "ROOT_DIR="+root, "OUTPUT_DIR="); code != 0 {
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

func TestNextjsCollect_PacksTheServerLayoutTheDeployPathAccepts(t *testing.T) {
	bundle := packedServerArchive(t, "")
	if bundle.Server == nil || bundle.Server.Main != "worker.js" || bundle.Server.Framework != "nextjs" ||
		bundle.Server.CompatibilityDate != "2025-09-01" || !slices.Contains(bundle.Server.CompatibilityFlags, "nodejs_compat") {
		t.Errorf("server config %+v", bundle.Server)
	}
	if len(bundle.Modules) != 1 || len(bundle.Files) != 1 {
		t.Errorf("modules %d (README and the source map must stay out), assets %d", len(bundle.Modules), len(bundle.Files))
	}
}

// A monorepo app: its assets are under the root directory, not the repository root.
func TestNextjsCollect_FindsTheAssetsUnderTheRootDirectory(t *testing.T) {
	bundle := packedServerArchive(t, "apps/web")
	if len(bundle.Files) != 1 {
		t.Errorf("assets %d, want the one under apps/web", len(bundle.Files))
	}
}

func TestNextjsCollect_NoBundleIsNoOutput(t *testing.T) {
	work, scripts := t.TempDir(), scriptDir(t)
	nextProject(t, work, "")
	if code, out := runScript(t, work, scripts, "collect.sh", "FRAMEWORK=nextjs", "ROOT_DIR=", "OUTPUT_DIR="); code != exitNoOutput {
		t.Fatalf("exit %d, want %d:\n%s", code, exitNoOutput, out)
	}
}

func TestStaticBuild_StillPacksTheOutputFolder(t *testing.T) {
	work, scripts := t.TempDir(), scriptDir(t)
	write(t, filepath.Join(work, "src", "dist", "index.html"), "<html>")
	if code, out := runScript(t, work, scripts, "collect.sh", "ROOT_DIR=", "OUTPUT_DIR=dist"); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(work, "collect", "site.tar.gz")); err != nil {
		t.Error(err)
	}
}
