package k8s

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/sites"
)

// These run the real build scripts under bash against a temp /work, with the framework tools
// stubbed, so the Next.js preset's steps, refusals and packed layout are pinned without a cluster.

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

// runScript runs one script with the work folder and scripts folder redirected; it answers the exit code and output.
func runScript(t *testing.T, work, scripts, name string, env ...string) (int, string) {
	t.Helper()
	command := exec.Command("bash", filepath.Join(scripts, name))
	command.Env = append([]string{"PATH=" + os.Getenv("PATH"), "WORK_DIR=" + work, "SCRIPTS_DIR=" + scripts, "HOME=" + filepath.Join(work, "home"), "HOLD_SECONDS=0"}, env...)
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

// nextProject lays out a repository with stubbed OpenNext and wrangler that log their arguments.
func nextProject(t *testing.T, work string, openNextOutput string) string {
	t.Helper()
	project := filepath.Join(work, "src")
	write(t, filepath.Join(project, "package.json"), `{"scripts":{"build":"next build"}}`)
	tools := filepath.Join(project, "node_modules/.bin")
	write(t, filepath.Join(tools, "opennextjs-cloudflare"), "#!/bin/bash\necho \"opennext $*\" >> \"$WORK_DIR/calls\"\n"+
		"mkdir -p .open-next/assets\necho '<html>' > .open-next/assets/index.html\necho '"+openNextOutput+"'\n")
	write(t, filepath.Join(tools, "wrangler"), "#!/bin/bash\necho \"wrangler $*\" >> \"$WORK_DIR/calls\"\n"+
		"while [[ $# -gt 0 ]]; do [[ $1 == --outdir ]] && out=$2; shift; done\n"+
		"mkdir -p \"$out\"\necho 'export default {}' > \"$out/worker.js\"\necho '{}' > \"$out/worker.js.map\"\necho readme > \"$out/README.md\"\n")
	return project
}

func TestNextjsPreset_BuildsPopulatesAndBundlesInOrder(t *testing.T) {
	work, scripts := t.TempDir(), scriptDir(t)
	nextProject(t, work, "built")
	code, out := runScript(t, work, scripts, "build.sh", "FRAMEWORK=nextjs", "ROOT_DIR=")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	calls, _ := os.ReadFile(filepath.Join(work, "calls"))
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	want := []string{"opennext build", "opennext populateCache local", "wrangler deploy --dry-run --minify --outdir " + filepath.Join(work, "bundle")}
	if len(lines) != len(want) {
		t.Fatalf("calls %q, want %q", lines, want)
	}
	for i := range want {
		if !strings.HasPrefix(lines[i], want[i]) {
			t.Errorf("call %d = %q, want %q", i, lines[i], want[i])
		}
	}
	for _, generated := range []string{"open-next.config.ts", "wrangler.jsonc"} {
		if _, err := os.Stat(filepath.Join(work, "src", generated)); err != nil {
			t.Errorf("the preset did not write %s: %v", generated, err)
		}
	}
}

func TestNextjsPreset_RefusesNodeMiddleware(t *testing.T) {
	work, scripts := t.TempDir(), scriptDir(t)
	nextProject(t, work, "WARN Node.js middleware support is experimental in cloudflare")
	code, out := runScript(t, work, scripts, "build.sh", "FRAMEWORK=nextjs", "ROOT_DIR=")
	if code != exitFramework || !strings.Contains(out, "Node.js middleware") || !strings.Contains(out, "app container") {
		t.Fatalf("exit %d, want %d naming Node.js middleware and the app container:\n%s", code, exitFramework, out)
	}
}

func TestNextjsPreset_RefusesCacheOverridesThatNeedOurStorage(t *testing.T) {
	for _, override := range []string{"r2-incremental-cache", "kv-incremental-cache", "do-queue", "d1-next-tag-cache", "do-sharded-tag-cache"} {
		work, scripts := t.TempDir(), scriptDir(t)
		project := nextProject(t, work, "built")
		write(t, filepath.Join(project, "open-next.config.ts"),
			`import cache from "@opennextjs/cloudflare/overrides/x/`+override+`"; export default { default: { override: { incrementalCache: cache } } };`)
		code, out := runScript(t, work, scripts, "build.sh", "FRAMEWORK=nextjs", "ROOT_DIR=")
		if code != exitFramework || !strings.Contains(out, "open-next.config.ts") {
			t.Errorf("%s: exit %d, want %d naming the config:\n%s", override, code, exitFramework, out)
		}
		if calls, _ := os.ReadFile(filepath.Join(work, "calls")); len(calls) != 0 {
			t.Errorf("%s: a tool ran before the refusal: %s", override, calls)
		}
	}
}

func TestNextjsPreset_KeepsAUserConfigThatAsksForNothingOfOurs(t *testing.T) {
	work, scripts := t.TempDir(), scriptDir(t)
	project := nextProject(t, work, "built")
	own := `export default { default: {} }; // mine`
	write(t, filepath.Join(project, "open-next.config.ts"), own)
	if code, out := runScript(t, work, scripts, "build.sh", "FRAMEWORK=nextjs", "ROOT_DIR="); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if body, _ := os.ReadFile(filepath.Join(project, "open-next.config.ts")); string(body) != own {
		t.Errorf("the user's config was replaced: %s", body)
	}
}

func TestNextjsPreset_UsersBuildCommandRunsAndOpenNextSkipsItsOwn(t *testing.T) {
	work, scripts := t.TempDir(), scriptDir(t)
	nextProject(t, work, "built")
	code, out := runScript(t, work, scripts, "build.sh", "FRAMEWORK=nextjs", "ROOT_DIR=", "BUILD_COMMAND=echo mine >> $WORK_DIR/calls")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	calls, _ := os.ReadFile(filepath.Join(work, "calls"))
	if !strings.HasPrefix(string(calls), "mine\nopennext build --skipNextBuild") {
		t.Errorf("calls:\n%s", calls)
	}
}

func TestNextjsCollect_PacksTheServerLayoutTheDeployPathAccepts(t *testing.T) {
	work, scripts := t.TempDir(), scriptDir(t)
	nextProject(t, work, "built")
	if code, out := runScript(t, work, scripts, "build.sh", "FRAMEWORK=nextjs", "ROOT_DIR="); code != 0 {
		t.Fatalf("build exit %d:\n%s", code, out)
	}
	if code, out := runScript(t, work, scripts, "collect.sh", "FRAMEWORK=nextjs", "ROOT_DIR=", "OUTPUT_DIR="); code != 0 {
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
	if bundle.Server == nil || bundle.Server.Main != "worker.js" || bundle.Server.Framework != "nextjs" {
		t.Errorf("server config %+v", bundle.Server)
	}
	if len(bundle.Modules) != 1 || len(bundle.Files) != 1 {
		t.Errorf("modules %d (README and the source map must stay out), assets %d", len(bundle.Modules), len(bundle.Files))
	}
}

func TestNextjsCollect_NoBundleIsNoOutput(t *testing.T) {
	work, scripts := t.TempDir(), scriptDir(t)
	nextProject(t, work, "built")
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
