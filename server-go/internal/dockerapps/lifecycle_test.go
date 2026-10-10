package dockerapps

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

func TestPauseStopsAndResumeStartsTheNewestDeploy(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	h.rollout(freeApp(), "dep-1")
	if _, err := h.rt.PausedAppReplicas(ctx, testDB, "app-01", "web"); !errors.Is(err, k8s.ErrAppNotPaused) {
		t.Fatalf("running app: err = %v, want ErrAppNotPaused", err)
	}
	if err := h.rt.PauseAppWorkload(ctx, testDB, "app-01"); err != nil {
		t.Fatal(err)
	}
	if err := h.rt.WaitForAppPodsGone(ctx, testDB, "app-01", time.Second); err != nil {
		t.Fatal(err)
	}
	if route := h.route(); !strings.Contains(route, "not running") || strings.Contains(route, "reverse_proxy") {
		t.Fatalf("a paused app's host must answer that it is not running:\n%s", route)
	}
	replicas, err := h.rt.PausedAppReplicas(ctx, testDB, "app-01", "web")
	if err != nil || replicas != 1 {
		t.Fatalf("paused replicas = %d (%v)", replicas, err)
	}
	if err := h.rt.ResumeAppWorkload(ctx, testDB, "app-01", "web", domain.Free, time.Second); err != nil {
		t.Fatal(err)
	}
	apps := h.engine.appContainers("app-01")
	if len(apps) != 1 || apps[0].state != "running" {
		t.Fatalf("after resume: %+v", apps)
	}
	if !strings.Contains(h.route(), apps[0].name+":8080") {
		t.Fatalf("resumed app not routed:\n%s", h.route())
	}
}

func TestResumeAtAnotherPlanRecreatesTheContainerAtItsSize(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	h.rollout(freeApp(), "dep-1")
	before := h.engine.appContainers("app-01")[0]
	if err := h.rt.PauseAppWorkload(ctx, testDB, "app-01"); err != nil {
		t.Fatal(err)
	}
	if err := h.rt.ResumeAppWorkload(ctx, testDB, "app-01", "web", domain.Standard, time.Second); err != nil {
		t.Fatal(err)
	}
	after := h.engine.appContainers("app-01")
	if len(after) != 1 || after[0].id == before.id || after[0].name != before.name {
		t.Fatalf("not recreated under its name: before %s after %+v", before.id, after)
	}
	if after[0].host.Memory != 1<<30 || after[0].config.Labels[labelTier] != "standard" || string(after[0].files[probePath]) != "probe" {
		t.Fatalf("recreated at memory %d tier %q", after[0].host.Memory, after[0].config.Labels[labelTier])
	}
	if strings.Join(after[0].config.Env, ",") != strings.Join(before.config.Env, ",") || after[0].config.Image != before.config.Image {
		t.Fatal("recreation changed what the container runs")
	}
}

func TestPauseAndResumeOfAnAppNeverDeployed(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	if err := h.rt.PauseAppWorkload(ctx, testDB, "nope"); !errors.Is(err, k8s.ErrAppNotDeployed) {
		t.Fatalf("pause: %v", err)
	}
	if err := h.rt.ResumeAppWorkload(ctx, testDB, "nope", "web", domain.Free, time.Second); !errors.Is(err, k8s.ErrAppNotDeployed) {
		t.Fatalf("resume: %v", err)
	}
}

func TestWithdrawTakesTheRoutesAwayAndRestoreBringsThemBack(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	app := freeApp()
	h.rollout(app, "dep-1")
	if err := h.rt.WithdrawProjectWorkloads(ctx, testDB, k8s.WithdrawOptions{AppRoutes: true}); err != nil {
		t.Fatal(err)
	}
	if h.route() != "" {
		t.Fatalf("route left after the withdrawal:\n%s", h.route())
	}
	if c := h.engine.appContainers("app-01")[0]; c.state == "running" {
		t.Fatal("app still running after the withdrawal")
	}
	if err := h.rt.ResumeAppWorkload(ctx, testDB, "app-01", "web", domain.Free, time.Second); err != nil {
		t.Fatal(err)
	}
	if h.route() != "" {
		t.Fatal("a resume alone routed a withdrawn app")
	}
	if err := h.rt.RestoreAppRoute(ctx, testDB, app, k8s.AppRouteOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.route(), h.engine.appContainers("app-01")[0].name) {
		t.Fatalf("route not restored:\n%s", h.route())
	}
}

func TestCustomDomainsAreRoutedBesideTheAppHost(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	app := freeApp()
	h.rollout(app, "dep-1")
	if err := h.rt.SyncAppDomains(ctx, testDB, app, []string{"shop.example.org", "www.shop.example.org"}, k8s.AppDomainOptions{}); err != nil {
		t.Fatal(err)
	}
	route := h.route()
	if !strings.Contains(route, "shop.example.org, www.shop.example.org {") || strings.Count(route, "reverse_proxy") != 2 {
		t.Fatalf("domains not routed to the app:\n%s", route)
	}
	for _, bad := range [][]string{{"studio.example.com"}, {"*.example.org"}, {"shop.example.org\n}\nevil {"}} {
		if err := h.rt.SyncAppDomains(ctx, testDB, app, bad, k8s.AppDomainOptions{}); err == nil {
			t.Errorf("%q routed", bad)
		}
	}
	if !strings.Contains(h.route(), "shop.example.org") {
		t.Fatal("a refused change replaced the route")
	}
	if err := h.rt.SyncAppDomains(ctx, testDB, app, nil, k8s.AppDomainOptions{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.route(), "shop.example.org") {
		t.Fatal("removed domain still routed")
	}
}

func TestPruneRemovesWhatTheAppRanUnderAnEarlierName(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	h.rollout(freeApp(), "dep-1")
	renamed := freeApp()
	renamed.Name = "shop"
	h.rollout(renamed, "dep-2")
	if err := h.rt.PruneAppWorkload(ctx, testDB, "app-01", "shop", time.Second); err != nil {
		t.Fatal(err)
	}
	apps := h.engine.appContainers("app-01")
	if len(apps) != 1 || apps[0].config.Labels[labelAppName] != "shop" {
		t.Fatalf("after prune: %+v", apps)
	}
	if route := h.route(); !strings.Contains(route, "shop-abc.apps.example.com") || strings.Contains(route, "web-abc") {
		t.Fatalf("route after rename:\n%s", route)
	}
}

func TestDeleteRemovesEverythingAndTheNetworkWithTheLastApp(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	app := freeApp()
	app.Disk = nil
	h.rollout(app, "dep-1")
	h.engine.volumes["excalibase-vol-app-disk-app-01"] = map[string]string{labelManaged: "true", labelApp: "app-01"}
	if err := h.rt.DeleteAppWorkload(ctx, testDB, "app-01", time.Second); err != nil {
		t.Fatal(err)
	}
	if len(h.engine.appContainers("app-01")) != 0 || h.route() != "" || len(h.engine.volumes) != 0 {
		t.Fatalf("left behind: containers %d route %q volumes %v", len(h.engine.appContainers("app-01")), h.route(), h.engine.volumes)
	}
	if _, ok := h.engine.networks[testNet]; ok {
		t.Fatal("the project's network outlived its last app")
	}
	if db := h.engine.byName("excalibase-proj-abc-postgres"); len(db.networks) != 0 {
		t.Fatalf("database still on %v", db.networks)
	}
}

func TestAWorkloadOfAnotherProjectIsRefused(t *testing.T) {
	h := newHarness(t, nil)
	app := freeApp()
	app.ProjectID = "proj-other"
	opts := renderOptions()
	workload, err := k8s.RenderAppWorkload(testDB, app, stubResolver{}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.rt.ApplyAppWorkload(context.Background(), testDB, workload); err == nil {
		t.Fatal("another project's workload ran in this project's scope")
	}
}

// The edge refuses a configuration naming one host twice, and would not start again with it.
func TestAHostAnotherAppServesIsNeverRoutedTwice(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	first := freeApp()
	h.rollout(first, "dep-1")
	second := freeApp()
	second.ID, second.Name = "app-02", "shop"
	h.rollout(second, "dep-2")
	if err := h.rt.SyncAppDomains(ctx, testDB, first, []string{"shop.example.org"}, k8s.AppDomainOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := h.rt.SyncAppDomains(ctx, testDB, second, []string{"shop.example.org"}, k8s.AppDomainOptions{}); err == nil {
		t.Fatal("a second app was routed a host the first serves")
	}
	if err := h.rt.SyncAppDomains(ctx, testDB, second, []string{"web-abc.apps.example.com"}, k8s.AppDomainOptions{}); err == nil {
		t.Fatal("a custom domain took another app's own hostname")
	}
	if err := h.rt.SyncAppDomains(ctx, testDB, second, []string{"x.apps.example.com"}, k8s.AppDomainOptions{}); err == nil {
		t.Fatal("a custom domain under the app domain was routed")
	}
}

// A recreated edge (an upgrade) is on no project network; the reconcile puts it back.
func TestReconcileRejoinsTheEdgeAndTheDatabase(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	h.rollout(freeApp(), "dep-1")
	for _, member := range []string{"edge01", testDB} {
		if err := h.engine.NetworkDisconnect(ctx, testNet, member, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.rt.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	members := h.engine.networks[testNet].members
	if !slices.Contains(members, "edge01") || !slices.Contains(members, testDB) {
		t.Fatalf("members after the reconcile: %v", members)
	}
}

// Every value in a route file is one the edge cannot read as a directive.
func TestRouteRefusesNamesThatCouldBreakTheEdgeConfig(t *testing.T) {
	h := newHarness(t, nil)
	for _, record := range []*appRecord{
		{Project: testProject, AppID: "app-01", AppName: "web\n}\nevil {", Port: 8080, Host: "web-abc.apps.example.com"},
		{Project: "proj abc", AppID: "app-01", AppName: "web", Port: 8080, Host: "web-abc.apps.example.com"},
	} {
		if _, err := h.rt.renderRoute(record); err == nil {
			t.Errorf("rendered %+v", record)
		}
	}
}

func TestRecordsStayInTheRoutesDirectory(t *testing.T) {
	h := newHarness(t, nil)
	if _, err := h.rt.loadRecord("..", "../etc/passwd"); err == nil {
		t.Fatal("read a record outside the routes directory")
	}
	if err := h.rt.removeRecord(testProject, "../x"); err == nil {
		t.Fatal("removed a file outside the routes directory")
	}
}

// A project whose database is gone is reported, and the loop keeps going until stopped.
func TestKeepReconciledReportsAProjectItCannotPlaceAndStopsWithItsContext(t *testing.T) {
	h := newHarness(t, nil)
	h.rollout(freeApp(), "dep-1")
	h.rt.opts.Projects = fakeScopes{}
	ctx, stop := context.WithCancel(context.Background())
	reports := 0
	done := make(chan struct{})
	go func() {
		h.rt.KeepReconciled(ctx, time.Millisecond, func(err error) {
			if reports++; reports == 2 {
				stop()
			}
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the reconcile loop did not stop with its context")
	}
	if reports < 2 {
		t.Fatalf("reports: %d", reports)
	}
}

// Both engines word membership differently; only these answers mean "already done".
func TestNetworkMembershipAnswersOfBothEngines(t *testing.T) {
	for message, want := range map[string]bool{
		"endpoint with name db already exists in network n": true, // Docker
		"container db is already connected to network n":    true, // Podman
		"network n not found":                               false,
	} {
		if got := alreadyMember(errors.New(message)); got != want {
			t.Errorf("alreadyMember(%q) = %v", message, got)
		}
	}
	for message, want := range map[string]bool{
		"container edge is not connected to network n": true, // Podman, a repeated disconnect
		"permission denied":                            false,
	} {
		if got := notMember(errors.New(message)); got != want {
			t.Errorf("notMember(%q) = %v", message, got)
		}
	}
}
