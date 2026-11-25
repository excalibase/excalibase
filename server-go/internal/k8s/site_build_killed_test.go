package k8s

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A build command the kernel kills (the memory limit) must leave the script as 137, not as a plain
// build failure: on a single host the engine's own out-of-memory flag can lag the exit, and the
// exit code is what still names the memory limit.
func TestBuildScript_ACommandKilledByTheKernelExits137(t *testing.T) {
	cases := []struct {
		name, command string
		want          int
	}{
		{"killed", `kill -9 $$`, 137},
		{"failed", `exit 3`, 11},
		{"passed", `true`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			work, scripts := t.TempDir(), scriptDir(t)
			if err := os.MkdirAll(filepath.Join(work, "src"), 0o755); err != nil {
				t.Fatal(err)
			}
			code, out := runScript(t, work, scripts, "build.sh", "ROOT_DIR=", "INSTALL_COMMAND=true", "BUILD_COMMAND="+tc.command)
			if code != tc.want {
				t.Errorf("exit %d, want %d:\n%s", code, tc.want, out)
			}
		})
	}
}

// Every command in the script that runs the app's code or tools must pass a kernel kill on, not only the
// plain build: the next build inside OpenNext is the most memory-hungry step of all.
func TestBuildScript_EveryKillPathExits137(t *testing.T) {
	killed := func(path string) {
		t.Helper()
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		write(t, path, strings.ReplaceAll(string(body), "exit 1", "kill -9 $$"))
	}
	cases := []struct {
		name string
		run  func(t *testing.T, work, scripts string) (int, string)
	}{
		{"install", func(t *testing.T, work, scripts string) (int, string) {
			mustMkdir(t, filepath.Join(work, "src"))
			return runScript(t, work, scripts, "build.sh", "ROOT_DIR=", "INSTALL_COMMAND=kill -9 $$")
		}},
		{"opennext build command", func(t *testing.T, work, scripts string) (int, string) {
			nextProject(t, work, "")
			return build(t, work, scripts, "BUILD_COMMAND=kill -9 $$")
		}},
		{"opennext build", func(t *testing.T, work, scripts string) (int, string) {
			killed(filepath.Join(nextProject(t, work, "", "opennext"), "node_modules/.bin/opennextjs-cloudflare"))
			return build(t, work, scripts)
		}},
		{"opennext populate cache", func(t *testing.T, work, scripts string) (int, string) {
			killed(filepath.Join(nextProject(t, work, "", "populate"), "node_modules/.bin/opennextjs-cloudflare"))
			return build(t, work, scripts)
		}},
		{"opennext wrangler bundle", func(t *testing.T, work, scripts string) (int, string) {
			killed(filepath.Join(nextProject(t, work, "", "wrangler"), "node_modules/.bin/wrangler"))
			return build(t, work, scripts)
		}},
		{"worker preset build command", func(t *testing.T, work, scripts string) (int, string) {
			presetProject(t, work, presets["astro"], true, false)
			return runScript(t, work, scripts, "build.sh", "FRAMEWORK=astro", "ROOT_DIR=", "INSTALL_COMMAND=true", "BUILD_COMMAND=kill -9 $$")
		}},
		{"worker preset wrangler bundle", func(t *testing.T, work, scripts string) (int, string) {
			project := presetProject(t, work, presets["astro"], true, false)
			write(t, filepath.Join(project, "node_modules/.bin/wrangler"), "#!/bin/bash\nkill -9 $$\n")
			return presetBuild(t, work, scripts, "astro")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := tc.run(t, t.TempDir(), scriptDir(t))
			if code != 137 {
				t.Errorf("exit %d, want 137:\n%s", code, out)
			}
		})
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}
