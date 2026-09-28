package apphost_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

func withDisk(mountPath, size string) *apphost.App {
	app := validApp()
	app.Disk = &apphost.AppDisk{MountPath: mountPath, Size: size}
	return app
}

func TestValidateAcceptsADisk(t *testing.T) {
	for _, path := range []string{"/data", "/var/lib/redis", "/usr/share/nginx/html", "/home/app/.cache", "/srv/k8s_state-1"} {
		if err := withDisk(path, "1Gi").Validate(); err != nil {
			t.Errorf("mount path %q refused: %v", path, err)
		}
	}
}

// A disk mounted over the image's system directories or the kernel's
// filesystems would break the container rather than hold its data.
func TestValidateRefusesADiskMountPathThatIsNotADataDirectory(t *testing.T) {
	refused := []string{
		"", "data", "/", "//data", "/data/", "/data/../etc", "/./data", "/da ta", "/data:ro",
		"/proc", "/proc/self", "/sys/fs", "/dev", "/dev/shm",
		"/etc", "/bin", "/sbin", "/lib", "/lib64", "/usr", "/var", "/run", "/var/run", "/boot",
		"/" + strings.Repeat("a", 256),
	}
	for _, path := range refused {
		if err := withDisk(path, "1Gi").Validate(); err == nil {
			t.Errorf("mount path %q accepted", path)
		}
	}
}

// Sizes are whole gibibytes, as a database disk's are (EXC-492).
func TestValidateDiskSizeIsWholeGibibytes(t *testing.T) {
	for _, size := range []string{"1Gi", "20Gi", "100Gi"} {
		if err := withDisk("/data", size).Validate(); err != nil {
			t.Errorf("size %q refused: %v", size, err)
		}
	}
	for _, size := range []string{"", "0Gi", "01Gi", "1.5Gi", "512Mi", "1G", "1Ti", "-1Gi", "1000000Gi"} {
		if err := withDisk("/data", size).Validate(); err == nil {
			t.Errorf("size %q accepted", size)
		}
	}
}

// One RWO volume cannot back several copies, so an app with a disk runs one.
func TestValidateAnAppWithADiskRunsAtMostOneCopy(t *testing.T) {
	for _, replicas := range []int{0, 1} {
		app := withDisk("/data", "1Gi")
		app.Replicas = replicas
		if err := app.Validate(); err != nil {
			t.Errorf("replicas %d refused: %v", replicas, err)
		}
	}
	app := withDisk("/data", "1Gi")
	app.Replicas = 2
	err := app.Validate()
	if err == nil || !strings.Contains(err.Error(), "one copy") {
		t.Fatalf("two copies with a disk: got %v, want a refusal naming one copy", err)
	}
}

func TestDiskGiB(t *testing.T) {
	disk := apphost.AppDisk{MountPath: "/data", Size: "20Gi"}
	if got, err := disk.GiB(); err != nil || got != 20 {
		t.Fatalf("GiB() = %d, %v; want 20", got, err)
	}
	if _, err := (apphost.AppDisk{Size: "1.5Gi"}).GiB(); err == nil {
		t.Fatal("an unreadable size must be an error")
	}
}

func TestCheckDiskWithinPlan(t *testing.T) {
	disk := &apphost.AppDisk{MountPath: "/data", Size: "20Gi"}
	if err := apphost.CheckDiskWithinPlan(disk, 20); err != nil {
		t.Fatalf("at the plan's cap: %v", err)
	}
	err := apphost.CheckDiskWithinPlan(disk, 19)
	if !errors.Is(err, apphost.ErrDiskAbovePlan) || !strings.Contains(err.Error(), "19Gi") {
		t.Fatalf("over the cap: got %v, want ErrDiskAbovePlan naming 19Gi", err)
	}
	if err := apphost.CheckDiskWithinPlan(disk, 0); !errors.Is(err, apphost.ErrDiskAbovePlan) {
		t.Fatalf("a plan with no app disks: got %v, want ErrDiskAbovePlan", err)
	}
	if err := apphost.CheckDiskWithinPlan(nil, 0); err != nil {
		t.Fatalf("no disk is always within the plan: %v", err)
	}
}

func TestPlanDiskGiBReadsWholeGibibytes(t *testing.T) {
	for size, want := range map[string]int{"0Gi": 0, "1Gi": 1, "100Gi": 100} {
		if got, err := apphost.PlanDiskGiB(size); err != nil || got != want {
			t.Errorf("PlanDiskGiB(%q) = %d, %v; want %d", size, got, err, want)
		}
	}
	for _, size := range []string{"", "1.5Gi", "1Ti", "512Mi", "abc"} {
		if _, err := apphost.PlanDiskGiB(size); err == nil {
			t.Errorf("PlanDiskGiB(%q) accepted", size)
		}
	}
}

// A disk is kept on the record and frozen into the deploy's masked view, so
// the history shows what each deploy mounted.
func TestDiskRoundTripsThroughJSON(t *testing.T) {
	app := withDisk("/data", "5Gi")
	encoded, err := json.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	var back apphost.App
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatal(err)
	}
	if back.Disk == nil || *back.Disk != *app.Disk {
		t.Fatalf("disk after a round trip = %+v, want %+v", back.Disk, app.Disk)
	}
	plain, _ := json.Marshal(validApp())
	if strings.Contains(string(plain), `"disk"`) {
		t.Fatalf("an app without a disk must not carry one: %s", plain)
	}
}

// A redeploy runs the frozen config but always with the app's current disk:
// the disk is state, and an older config must never drop or duplicate it.
func TestToAppCarriesNoDiskOfItsOwn(t *testing.T) {
	app := withDisk("/data", "5Gi")
	rebuilt := apphost.ConfigFromApp(app).ToApp(app.ID, app.ProjectID, app.Name)
	if rebuilt.Disk != nil {
		t.Fatalf("a frozen config must not carry a disk: %+v", rebuilt.Disk)
	}
}
