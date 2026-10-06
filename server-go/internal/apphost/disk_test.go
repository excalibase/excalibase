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
	for _, path := range []string{"/data", "/var/lib/redis", "/var/lib/postgresql/data", "/home/app/.cache", "/srv/k8s_state-1", "/usrdata", "/etcetera", "/running"} {
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
		// Under a system directory hides the image's files just as surely.
		"/usr/local/data", "/usr/share/nginx/html", "/etc/app", "/bin/x", "/sbin/x", "/lib/x", "/lib64/x",
		"/boot/x", "/run/app", "/var/run/app",
	}
	for _, path := range refused {
		if err := withDisk(path, "1Gi").Validate(); err == nil {
			t.Errorf("mount path %q accepted", path)
		}
	}
}

// Sizes are whole mebibytes or gibibytes: a disk lowered to fit a smaller
// plan (the owner's downgrade rule) may land on, say, 500Mi.
func TestValidateDiskSizeIsWholeMebibytesOrGibibytes(t *testing.T) {
	for _, size := range []string{"64Mi", "500Mi", "1Gi", "20Gi", "100Gi"} {
		if err := withDisk("/data", size).Validate(); err != nil {
			t.Errorf("size %q refused: %v", size, err)
		}
	}
	for _, size := range []string{"", "0Gi", "0Mi", "63Mi", "01Gi", "1.5Gi", "1G", "1Ti", "-1Gi", "1000000Gi", "10000000Mi", "1Ki"} {
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

func TestDiskBytes(t *testing.T) {
	for size, want := range map[string]int64{"20Gi": 20 << 30, "500Mi": 500 << 20} {
		disk := apphost.AppDisk{MountPath: "/data", Size: size}
		if got, err := disk.Bytes(); err != nil || got != want {
			t.Errorf("Bytes(%q) = %d, %v; want %d", size, got, err, want)
		}
	}
	if _, err := (apphost.AppDisk{Size: "1.5Gi"}).Bytes(); err == nil {
		t.Fatal("an unreadable size must be an error")
	}
}

func TestCheckDiskWithinPlan(t *testing.T) {
	disk := &apphost.AppDisk{MountPath: "/data", Size: "20Gi"}
	if err := apphost.CheckDiskWithinPlan(disk, 20<<30); err != nil {
		t.Fatalf("at the plan's cap: %v", err)
	}
	err := apphost.CheckDiskWithinPlan(disk, 19<<30)
	if !errors.Is(err, apphost.ErrDiskAbovePlan) || !strings.Contains(err.Error(), "19Gi") {
		t.Fatalf("over the cap: got %v, want ErrDiskAbovePlan naming 19Gi", err)
	}
	err = apphost.CheckDiskWithinPlan(&apphost.AppDisk{MountPath: "/data", Size: "1Gi"}, 500<<20)
	if !errors.Is(err, apphost.ErrDiskAbovePlan) || !strings.Contains(err.Error(), "500Mi") {
		t.Fatalf("over a mebibyte cap: got %v, want ErrDiskAbovePlan naming 500Mi", err)
	}
	if err := apphost.CheckDiskWithinPlan(disk, 0); !errors.Is(err, apphost.ErrDiskAbovePlan) {
		t.Fatalf("a plan with no app disks: got %v, want ErrDiskAbovePlan", err)
	}
	if err := apphost.CheckDiskWithinPlan(nil, 0); err != nil {
		t.Fatalf("no disk is always within the plan: %v", err)
	}
}

func TestPlanDiskBytesReadsWholeMebibytesOrGibibytes(t *testing.T) {
	for size, want := range map[string]int64{"0Gi": 0, "0Mi": 0, "1Gi": 1 << 30, "100Gi": 100 << 30, "500Mi": 500 << 20} {
		if got, err := apphost.PlanDiskBytes(size); err != nil || got != want {
			t.Errorf("PlanDiskBytes(%q) = %d, %v; want %d", size, got, err, want)
		}
	}
	for _, size := range []string{"", "1.5Gi", "1Ti", "63Mi", "abc", "1G"} {
		if _, err := apphost.PlanDiskBytes(size); err == nil {
			t.Errorf("PlanDiskBytes(%q) accepted", size)
		}
	}
}

// Sizes are shown the way they are written: whole gibibytes when they are,
// otherwise mebibytes rounded up, so a usage never reads smaller than it is.
func TestFormatDiskSize(t *testing.T) {
	for bytes, want := range map[int64]string{
		0: "0Mi", 1: "1Mi", 100 << 20: "100Mi", (100 << 20) + 1: "101Mi", 1 << 30: "1Gi", 1536 << 20: "1536Mi", 20 << 30: "20Gi",
	} {
		if got := apphost.FormatDiskSize(bytes); got != want {
			t.Errorf("FormatDiskSize(%d) = %q, want %q", bytes, got, want)
		}
	}
}

// The generation is the platform's: which volume holds the disk after it was
// moved onto a smaller one. A caller cannot name another volume through it.
func TestValidateDiskGeneration(t *testing.T) {
	app := withDisk("/data", "1Gi")
	app.Disk.Generation = 3
	if err := app.Validate(); err != nil {
		t.Fatalf("generation 3 refused: %v", err)
	}
	for _, generation := range []int{-1, 1000000} {
		app.Disk.Generation = generation
		if err := app.Validate(); err == nil {
			t.Errorf("generation %d accepted", generation)
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
