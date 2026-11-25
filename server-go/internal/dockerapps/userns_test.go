package dockerapps

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// poolMaps is what a container prints of /proc/self/uid_map and gid_map when the
// platform user owns the default 65536 ids plus room for apps of 65536 each.
func poolMaps(apps int) string {
	total := 65536 * (apps + 1)
	table := fmt.Sprintf("         0       1000          1\n         1     100000 %10d\n", total)
	return table + "---\n" + table
}

func TestParseIDMapsReadsTheContiguousPoolOfBothMaps(t *testing.T) {
	pool, err := parseIDMaps(poolMaps(3))
	if err != nil || pool.uidEnd != 65536*4+1 || pool.gidEnd != 65536*4+1 {
		t.Fatalf("pool %+v, err %v", pool, err)
	}
	split := "0 1000 1\n1 100000 65536\n65537 400000 65536\n---\n0 1000 1\n1 100000 65536\n"
	pool, err = parseIDMaps(split)
	if err != nil || pool.uidEnd != 131073 || pool.gidEnd != 65537 {
		t.Fatalf("two entries: pool %+v, err %v (the smaller map limits)", pool, err)
	}
	gap := "0 1000 1\n1 100000 65536\n70000 400000 65536\n---\n0 1000 1\n1 100000 65536\n"
	if pool, _ := parseIDMaps(gap); pool.uidEnd != 65537 {
		t.Fatalf("a gap in the map ends the pool: %+v", pool)
	}
	for _, bad := range []string{"", "0 1000 1\n", "x y z\n---\n0 1 1\n"} {
		if _, err := parseIDMaps(bad); err == nil {
			t.Fatalf("%q was read as a pool", bad)
		}
	}
}

const fixHint = "init.sh"

type planCase struct {
	name, setting string
	host          hostKind
	pool          idPool
}

var (
	bigPool   = idPool{uidEnd: 65536*4 + 1, gidEnd: 65536*4 + 1}
	smallPool = idPool{uidEnd: 65537, gidEnd: 65537}
	podmanBox = hostKind{rootlessPodman: true}
)

func planFor(c planCase) (usernsPlan, error) {
	return planUserns(c.setting, c.host, c.pool, defaultUsernsFirstBase, defaultUsernsRangeSize)
}

func TestPlanUsernsGivesRangesOnlyWhereTheyApply(t *testing.T) {
	cases := []struct {
		planCase
		perApp bool
		slots  int
		notice string
	}{
		{planCase: planCase{"shared stays shared", UsernsShared, podmanBox, bigPool}},
		{planCase: planCase{"auto with room", UsernsAuto, podmanBox, bigPool}, perApp: true, slots: 3},
		{planCase: planCase{"per-app with room", UsernsPerApp, podmanBox, bigPool}, perApp: true, slots: 3},
		{planCase: planCase{"auto without room falls back and says how", UsernsAuto, podmanBox, smallPool}, notice: fixHint},
		{planCase: planCase{"auto on docker stays shared quietly", UsernsAuto, hostKind{}, bigPool}},
		{planCase: planCase{"auto under a sandbox stays shared quietly", UsernsAuto, hostKind{rootlessPodman: true, sandboxed: true}, bigPool}},
		{planCase: planCase{"unset is shared", "", podmanBox, bigPool}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan, err := planFor(c.planCase)
			if err != nil || plan.perApp != c.perApp || plan.slots != c.slots {
				t.Fatalf("plan %+v, err %v; want perApp %v slots %d", plan, err, c.perApp, c.slots)
			}
			if (c.notice == "") != (plan.notice == "") || !strings.Contains(plan.notice, c.notice) {
				t.Fatalf("notice %q, want one mentioning %q", plan.notice, c.notice)
			}
		})
	}
}

func TestPlanUsernsRefusesPerAppWhereItCannotApply(t *testing.T) {
	cases := []struct {
		planCase
		mention string
	}{
		{planCase{"without room", UsernsPerApp, podmanBox, smallPool}, fixHint},
		{planCase{"on docker", UsernsPerApp, hostKind{}, bigPool}, "rootless Podman"},
		{planCase{"under a sandbox", UsernsPerApp, hostKind{rootlessPodman: true, sandboxed: true}, bigPool}, "without a sandbox"},
		{planCase{"misspelt", "per_app", podmanBox, bigPool}, "APP_USERNS"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := planFor(c.planCase); err == nil || !strings.Contains(err.Error(), c.mention) {
				t.Fatalf("err = %v, want it to mention %q", err, c.mention)
			}
		})
	}
}

func TestFreeRangeNeverOverlapsAndReusesTheLowestFree(t *testing.T) {
	first, size := uint32(65537), uint32(65536)
	slot := func(k uint32) usernsRange { return usernsRange{Base: first + k*size, Size: size} }
	got, ok := freeRange(nil, first, size, 3)
	if !ok || got != slot(0) {
		t.Fatalf("first range %+v %v", got, ok)
	}
	got, _ = freeRange([]usernsRange{slot(0), slot(2)}, first, size, 3)
	if got != slot(1) {
		t.Fatalf("lowest free %+v, want slot 1", got)
	}
	if _, ok := freeRange([]usernsRange{slot(0), slot(1), slot(2)}, first, size, 3); ok {
		t.Fatal("a full pool gave a range")
	}
	// A range left from another size still blocks every slot it touches.
	odd := usernsRange{Base: first + 100, Size: 10}
	got, _ = freeRange([]usernsRange{odd}, first, size, 3)
	if got != slot(1) {
		t.Fatalf("a range inside slot 0 left it taken: got %+v", got)
	}
}

func TestUsernsModeIsOneExplicitMappingOfRootToTheRange(t *testing.T) {
	got := usernsRange{Base: 131073, Size: 65536}.mode()
	if want := "auto:uidmapping=0:131073:65536,gidmapping=0:131073:65536"; string(got) != want {
		t.Fatalf("mode %q, want %q", got, want)
	}
	back, ok := parseUsernsLabel(usernsRange{Base: 131073, Size: 65536}.label())
	if !ok || back.Base != 131073 || back.Size != 65536 {
		t.Fatalf("label round trip %+v %v", back, ok)
	}
	for _, bad := range []string{"", "1", "a:b", "0:65536", "1:0", "1:2:3"} {
		if _, ok := parseUsernsLabel(bad); ok {
			t.Fatalf("label %q was accepted", bad)
		}
	}
}

// unsandboxedPodman is a harness on rootless Podman with no sandbox and a pool for apps app ranges.
func unsandboxedPodman(t *testing.T, setting string, apps int) *harness {
	t.Helper()
	h := newHarness(t, func(o *Options) { o.SandboxRuntime, o.Userns, o.Isolate = "", setting, true })
	h.engine.podman = true
	h.engine.toolOutput = func(cmd []string) (string, int) {
		if slices.Contains(cmd, "/proc/self/uid_map") || strings.Contains(strings.Join(cmd, " "), "uid_map") {
			return poolMaps(apps), 0
		}
		if cmd[0] == "du" {
			return "4\t/disk\n", 0
		}
		return "", 0
	}
	return h
}

func appNamed(id string) *apphost.App {
	app := diskApp()
	app.ID, app.Name = id, "web-"+id
	return app
}

func (h *harness) createDisk(app *apphost.App) {
	h.t.Helper()
	if err := h.rt.CreateAppDisk(context.Background(), testDB, app, "", diskJobs); err != nil {
		h.t.Fatalf("disk of %s: %v", app.ID, err)
	}
}

func (h *harness) rangeOf(appID string) string {
	h.t.Helper()
	apps := h.engine.appContainers(appID)
	if len(apps) == 0 {
		h.t.Fatalf("app %s has no container", appID)
	}
	return string(apps[0].host.UsernsMode)
}

func TestPerAppAppsGetDistinctStableRangesAcrossRedeploys(t *testing.T) {
	h := unsandboxedPodman(t, UsernsPerApp, 4)
	if err := h.rt.VerifyEngine(context.Background()); err != nil {
		t.Fatal(err)
	}
	a, b := appNamed("app-a"), appNamed("app-b")
	for _, app := range []*apphost.App{a, b} {
		h.createDisk(app)
		h.deploy(app, "dep-1")
	}
	rangeA, rangeB := h.rangeOf("app-a"), h.rangeOf("app-b")
	if rangeA == "" || rangeB == "" || rangeA == rangeB {
		t.Fatalf("ranges %q and %q, want two distinct ones", rangeA, rangeB)
	}
	if want := "auto:uidmapping=0:65537:65536,gidmapping=0:65537:65536"; rangeA != want {
		t.Fatalf("the first app got %q, want %q", rangeA, want)
	}
	for _, name := range []string{"excalibase-vol-app-disk-app-a", "excalibase-vol-app-disk-app-b"} {
		if h.engine.volumes[name][labelUserns] == "" {
			t.Fatalf("volume %s does not carry its range: %v", name, h.engine.volumes[name])
		}
	}
	if h.engine.appContainers("app-a")[0].config.Labels[labelUserns] != "65537:65536" {
		t.Fatalf("container labels %v", h.engine.appContainers("app-a")[0].config.Labels)
	}
	h.deploy(a, "dep-2")
	if again := h.rangeOf("app-a"); again != rangeA {
		t.Fatalf("a redeploy moved the app from %q to %q", rangeA, again)
	}
}

func TestPerAppRangeSurvivesARestartOfThePlatformAndAResume(t *testing.T) {
	h := unsandboxedPodman(t, UsernsPerApp, 4)
	_ = h.rt.VerifyEngine(context.Background())
	app := appNamed("app-a")
	h.createDisk(app)
	h.deploy(app, "dep-1")
	before := h.rangeOf("app-a")

	// A new process knows nothing but the record and what the engine holds.
	again, err := New(h.engine, h.rt.opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := again.VerifyEngine(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.rt = again
	h.deploy(app, "dep-2")
	if got := h.rangeOf("app-a"); got != before {
		t.Fatalf("after a platform restart the app is in %q, was in %q", got, before)
	}
	if err := again.PauseAppWorkload(context.Background(), testDB, "app-a"); err != nil {
		t.Fatal(err)
	}
	if err := again.ResumeAppWorkload(context.Background(), testDB, "app-a", app.Name, domain.Standard, 0); err != nil {
		t.Fatal(err)
	}
	if got := h.rangeOf("app-a"); got != before {
		t.Fatalf("a resize on resume put the app in %q, was in %q", got, before)
	}
}

func TestADiskToolRunsInTheRangeOfTheAppWhoseFilesItTouches(t *testing.T) {
	h := unsandboxedPodman(t, UsernsPerApp, 2)
	_ = h.rt.VerifyEngine(context.Background())
	var modes []string
	inner := h.engine.toolOutput
	h.engine.toolOutput = func(cmd []string) (string, int) {
		for _, c := range h.engine.containers {
			if c.config.Labels[labelComponent] == componentDiskTool {
				modes = append(modes, string(c.host.UsernsMode))
			}
		}
		return inner(cmd)
	}
	app := appNamed("app-a")
	h.createDisk(app)
	if _, err := h.rt.AppDiskUsage(context.Background(), testDB, app.ID, *app.Disk, diskJobs); err != nil {
		t.Fatal(err)
	}
	want := "auto:uidmapping=0:65537:65536,gidmapping=0:65537:65536"
	if len(modes) != 2 || modes[0] != want || modes[1] != want {
		t.Fatalf("tool ranges %q, want both in %q", modes, want)
	}
}

func TestARangeIsNotReusedWhileAnOldVolumeStillHoldsIt(t *testing.T) {
	h := unsandboxedPodman(t, UsernsPerApp, 1)
	_ = h.rt.VerifyEngine(context.Background())
	a := appNamed("app-a")
	h.createDisk(a)
	h.deploy(a, "dep-1")
	// Its app is gone, as a failed cleanup leaves it: no record, no container, the volume still there.
	if err := h.rt.removeRecord(testProject, "app-a"); err != nil {
		t.Fatal(err)
	}
	for id, c := range h.engine.containers {
		if c.config.Labels[labelApp] == "app-a" {
			delete(h.engine.containers, id)
		}
	}
	b := appNamed("app-b")
	err := h.rt.CreateAppDisk(context.Background(), testDB, b, "", diskJobs)
	if err == nil || !strings.Contains(err.Error(), "init.sh") {
		t.Fatalf("a range an old volume holds was handed out again: %v", err)
	}
	delete(h.engine.volumes, "excalibase-vol-app-disk-app-a")
	h.createDisk(b)
	if got := h.engine.volumes["excalibase-vol-app-disk-app-b"][labelUserns]; got != "65537:65536" {
		t.Fatalf("once the volume is gone its range is free again: %q", got)
	}
}

func TestConcurrentFirstDeploysGetDistinctRanges(t *testing.T) {
	h := unsandboxedPodman(t, UsernsPerApp, 8)
	_ = h.rt.VerifyEngine(context.Background())
	var wg sync.WaitGroup
	ranges := make([]string, 8)
	for i := range ranges {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := h.rt.appUserns(context.Background(), testProject, fmt.Sprintf("app-%d", i))
			if err != nil || got == nil {
				t.Errorf("app %d: %v %v", i, got, err)
				return
			}
			ranges[i] = got.label()
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, got := range ranges {
		if seen[got] {
			t.Fatalf("two apps share range %s: %v", got, ranges)
		}
		seen[got] = true
	}
}

// restartedWith is the platform started again with another APP_USERNS, the engine as it was.
func (h *harness) restartedWith(setting string) {
	h.t.Helper()
	h.rt.opts.Userns = setting
	again, err := New(h.engine, h.rt.opts)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := again.VerifyEngine(context.Background()); err != nil {
		h.t.Fatal(err)
	}
	h.rt = again
}

func (h *harness) requireRange(appID string, wantRange bool) {
	h.t.Helper()
	if got := h.rangeOf(appID); (got != "") != wantRange {
		h.t.Fatalf("app %s runs in range %q, want a range: %v", appID, got, wantRange)
	}
}

func TestAnAppThatAlreadyRunsSharedStaysSharedWhenPerAppIsTurnedOn(t *testing.T) {
	h := unsandboxedPodman(t, UsernsShared, 4)
	app := appNamed("app-a")
	h.createDisk(app)
	h.deploy(app, "dep-1")
	h.requireRange("app-a", false)

	h.restartedWith(UsernsPerApp)
	h.deploy(app, "dep-2")
	h.requireRange("app-a", false)
	fresh := appNamed("app-new")
	h.createDisk(fresh)
	h.deploy(fresh, "dep-1")
	h.requireRange("app-new", true)
}

// An app from before this release has no record of a decision, only what it runs: each signal alone keeps it shared.
func TestAnAppWhoseVolumePredatesRangesStaysShared(t *testing.T) {
	h := unsandboxedPodman(t, UsernsPerApp, 4)
	_ = h.rt.VerifyEngine(context.Background())
	h.engine.volumes["excalibase-vol-app-disk-app-old"] = map[string]string{labelManaged: "true", labelProject: testProject, labelApp: "app-old"}
	got, err := h.rt.appUserns(context.Background(), testProject, "app-old")
	if err != nil || got != nil {
		t.Fatalf("range %+v (%v) for an app with an unranged volume", got, err)
	}
}

func TestAnAppWhoseContainerPredatesRangesStaysShared(t *testing.T) {
	h := unsandboxedPodman(t, UsernsPerApp, 4)
	_ = h.rt.VerifyEngine(context.Background())
	h.engine.addContainer("old01", "excalibase-app-old", map[string]string{
		labelManaged: "true", labelComponent: componentApp, labelProject: testProject, labelApp: "app-old"})
	got, err := h.rt.appUserns(context.Background(), testProject, "app-old")
	if err != nil || got != nil {
		t.Fatalf("range %+v (%v) for an app with an unranged container", got, err)
	}
}

func TestAnAppWithADeployRecordedStaysShared(t *testing.T) {
	h := unsandboxedPodman(t, UsernsPerApp, 4)
	_ = h.rt.VerifyEngine(context.Background())
	if err := h.rt.updateRecord(testProject, "app-old", func(r *appRecord) error { r.Deploy = "dep-0"; return nil }); err != nil {
		t.Fatal(err)
	}
	got, err := h.rt.appUserns(context.Background(), testProject, "app-old")
	if err != nil || got != nil {
		t.Fatalf("range %+v (%v) for an app with a deploy", got, err)
	}
}

func TestSharedAndSandboxedAppsCarryNoRange(t *testing.T) {
	for name, mutate := range map[string]func(*Options){
		"shared":    func(o *Options) { o.SandboxRuntime, o.Userns, o.Isolate = "", UsernsShared, true },
		"unset":     func(o *Options) { o.SandboxRuntime, o.Userns, o.Isolate = "", "", true },
		"sandboxed": func(o *Options) { o.Userns, o.Isolate = UsernsAuto, true },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, mutate)
			h.engine.podman = true
			h.createDisk(appNamed("app-a"))
			h.deploy(appNamed("app-a"), "dep-1")
			if got := h.rangeOf("app-a"); got != "" {
				t.Fatalf("range %q", got)
			}
			if len(h.engine.volumes["excalibase-vol-app-disk-app-a"][labelUserns]) != 0 {
				t.Fatalf("volume labels %v", h.engine.volumes)
			}
		})
	}
}

func TestVerifyEngineAutoFallsBackToSharedWithOneNotice(t *testing.T) {
	h := unsandboxedPodman(t, UsernsAuto, 0)
	if err := h.rt.VerifyEngine(context.Background()); err != nil {
		t.Fatal(err)
	}
	notice := h.rt.UsernsNotice()
	if !strings.Contains(notice, "init.sh") || !strings.Contains(notice, "--grow-subids") {
		t.Fatalf("notice %q", notice)
	}
	h.createDisk(appNamed("app-a"))
	h.deploy(appNamed("app-a"), "dep-1")
	if got := h.rangeOf("app-a"); got != "" {
		t.Fatalf("auto without room gave range %q", got)
	}
}

func TestVerifyEnginePerAppRefusesAPoolTooSmall(t *testing.T) {
	h := unsandboxedPodman(t, UsernsPerApp, 0)
	err := h.rt.VerifyEngine(context.Background())
	if err == nil || !strings.Contains(err.Error(), "APP_USERNS") || !strings.Contains(err.Error(), "init.sh") {
		t.Fatalf("err = %v", err)
	}
}

func TestAnAppBeyondThePoolIsRefusedInAutoAsInPerApp(t *testing.T) {
	for _, setting := range []string{UsernsPerApp, UsernsAuto} {
		t.Run(setting, func(t *testing.T) {
			h := unsandboxedPodman(t, setting, 1)
			if err := h.rt.VerifyEngine(context.Background()); err != nil {
				t.Fatal(err)
			}
			h.createDisk(appNamed("app-a"))
			err := h.rt.CreateAppDisk(context.Background(), testDB, appNamed("app-b"), "", diskJobs)
			if !errors.Is(err, ErrUsernsPoolFull) || !strings.Contains(err.Error(), fixHint) {
				t.Fatalf("err = %v, want the full pool named with how to grow it", err)
			}
			if _, made := h.engine.volumes["excalibase-vol-app-disk-app-b"]; made {
				t.Fatal("a disk was made for an app that cannot be placed")
			}
		})
	}
}

func TestAnUnreadablePoolStopsPerAppButNotAuto(t *testing.T) {
	for setting, wantErr := range map[string]bool{UsernsPerApp: true, UsernsAuto: false} {
		t.Run(setting, func(t *testing.T) {
			h := unsandboxedPodman(t, setting, 4)
			inner := h.engine.toolOutput
			h.engine.toolOutput = func(cmd []string) (string, int) {
				if strings.Contains(strings.Join(cmd, " "), "uid_map") {
					return "cat: no such file", 1
				}
				return inner(cmd)
			}
			err := h.rt.VerifyEngine(context.Background())
			if (err != nil) != wantErr {
				t.Fatalf("err = %v, want an error: %v", err, wantErr)
			}
			if !wantErr && !strings.Contains(h.rt.UsernsNotice(), "id map") {
				t.Fatalf("notice %q", h.rt.UsernsNotice())
			}
		})
	}
}
