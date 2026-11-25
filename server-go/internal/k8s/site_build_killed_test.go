package k8s

import (
	"os"
	"path/filepath"
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
