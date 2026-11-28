package memlimit

import (
	"os"
	"path/filepath"
	"testing"
)

func cgroupFile(t *testing.T, relative, content string) string {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestContainerLimitReadsCgroupV2(t *testing.T) {
	root := cgroupFile(t, "memory.max", "268435456\n")
	got, ok := ContainerLimit(root, "")
	if !ok || got != 268435456 {
		t.Fatalf("limit = %d, %v; want 268435456", got, ok)
	}
}

func TestContainerLimitReadsCgroupV1(t *testing.T) {
	root := cgroupFile(t, "memory/memory.limit_in_bytes", "134217728\n")
	got, ok := ContainerLimit(root, "")
	if !ok || got != 134217728 {
		t.Fatalf("limit = %d, %v; want 134217728", got, ok)
	}
}

func TestContainerLimitIsAbsentWhenUnlimited(t *testing.T) {
	for name, root := range map[string]string{
		"v2 max":       cgroupFile(t, "memory.max", "max\n"),
		"v1 unlimited": cgroupFile(t, "memory/memory.limit_in_bytes", "9223372036854771712\n"),
		"garbage":      cgroupFile(t, "memory.max", "lots\n"),
		"no cgroup":    t.TempDir(),
	} {
		if got, ok := ContainerLimit(root, ""); ok {
			t.Errorf("%s: limit %d reported", name, got)
		}
	}
}

func TestSoftLimitIsAShareOfTheContainer(t *testing.T) {
	root := cgroupFile(t, "memory.max", "1000000\n")
	got, ok := SoftLimit(root, "", 80, "")
	if !ok || got != 800000 {
		t.Fatalf("soft limit = %d, %v; want 800000", got, ok)
	}
}

func TestSoftLimitYieldsToAnExplicitGOMEMLIMIT(t *testing.T) {
	root := cgroupFile(t, "memory.max", "1000000\n")
	if got, ok := SoftLimit(root, "", 80, "123MiB"); ok {
		t.Fatalf("soft limit %d set over an explicit GOMEMLIMIT", got)
	}
}

func TestSoftLimitRefusesAnImpossibleShare(t *testing.T) {
	root := cgroupFile(t, "memory.max", "1000000\n")
	for _, percent := range []int{0, -5, 101} {
		if got, ok := SoftLimit(root, "", percent, ""); ok {
			t.Errorf("percent %d gave limit %d", percent, got)
		}
	}
}

// Outside a cgroup namespace (a systemd scope, a bare host) the process's own cgroup is a
// directory below the root, named by /proc/self/cgroup.
func TestContainerLimitFollowsTheProcessCgroup(t *testing.T) {
	root := cgroupFile(t, "user.slice/run-x.scope/memory.max", "268435456\n")
	got, ok := ContainerLimit(root, "0::/user.slice/run-x.scope\n")
	if !ok || got != 268435456 {
		t.Fatalf("limit = %d, %v; want 268435456", got, ok)
	}
	if got, ok := ContainerLimit(root, "0::/\n"); ok {
		t.Fatalf("limit %d reported for the root cgroup", got)
	}
	if got, ok := ContainerLimit(root, "0::/../../etc\n"); ok {
		t.Fatalf("limit %d reported for a path leaving the root", got)
	}
}

func TestApplyLeavesAnExplicitGOMEMLIMITAlone(t *testing.T) {
	if got, ok := Apply(80, "256MiB"); ok {
		t.Fatalf("Apply set %d over an explicit GOMEMLIMIT", got)
	}
}

func TestApplyReportsNothingWithoutAContainerLimit(t *testing.T) {
	// The test host's own cgroup may or may not be limited; either way the result must agree with SoftLimit.
	selfCgroup, _ := os.ReadFile("/proc/self/cgroup")
	want, wantOK := SoftLimit(CgroupRoot, string(selfCgroup), 80, "")
	if got, ok := Apply(80, ""); ok != wantOK || got != want {
		t.Fatalf("Apply = %d, %v; want %d, %v", got, ok, want, wantOK)
	}
}
