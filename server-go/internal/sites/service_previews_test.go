package sites_test

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

type previewHarness struct {
	*serviceHarness
	previews *sitestest.MemoryPreviews
	now      time.Time
}

type previewLimits struct{ max int }

func (l previewLimits) DeployLimits(context.Context, string) (sites.Limits, error) {
	limits := sites.DefaultLimits
	limits.MaxPreviews = l.max
	return limits, nil
}

func newPreviewHarness(t *testing.T, perProject, perInstall int) *previewHarness {
	t.Helper()
	store, publisher, origins := sitestest.NewMemoryStore(), sitestest.NewPublisher(), sitestest.NewOrigins()
	previews := sitestest.NewMemoryPreviews(store)
	h := &previewHarness{previews: previews, now: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	service, err := sites.NewService(store, publisher, origins, sites.Options{BaseDomain: testDomain, SpoolDir: t.TempDir(),
		DeployLimits: previewLimits{max: perProject}, Previews: previews, MaxPreviews: perInstall})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	service.SetNow(func() time.Time { return h.now })
	store.Now = func() time.Time { return h.now }
	h.serviceHarness = &serviceHarness{service: service, store: store, publisher: publisher, origins: origins}
	return h
}

func (h *previewHarness) open(t *testing.T, site *sites.Site, source, ref string) (*sites.Preview, error) {
	t.Helper()
	return h.service.OpenPreview(context.Background(), sites.PreviewRequest{ProjectID: testProject, SiteID: site.ID,
		Source: source, Ref: ref, CommitSHA: "abc1234def", Actor: testActor})
}

func (h *previewHarness) mustOpen(t *testing.T, site *sites.Site, source, ref string) *sites.Preview {
	t.Helper()
	preview, err := h.open(t, site, source, ref)
	if err != nil {
		t.Fatalf("OpenPreview %s %s: %v", source, ref, err)
	}
	return preview
}

func (h *previewHarness) deployPreview(t *testing.T, preview *sites.Preview, apiAccess bool) (*sites.Preview, error) {
	t.Helper()
	return h.service.DeployPreview(context.Background(), sites.PreviewDeployRequest{ProjectID: testProject, SiteID: preview.SiteID,
		PreviewID: preview.ID, CommitSHA: "abc1234def", APIAccess: apiAccess, Body: bytes.NewReader(sitestest.Dist(t, "<h1>preview</h1>"))})
}

var previewLabel = regexp.MustCompile(`^shop--[a-z2-7]{26}$`)

func TestPreviewHostnameIsUnguessableAndNeverASiteName(t *testing.T) {
	h := newPreviewHarness(t, 5, 100)
	site := h.createSite(t, "shop")
	first := h.mustOpen(t, site, sites.PreviewSourceBranch, "feat/a")
	second := h.mustOpen(t, site, sites.PreviewSourceBranch, "feat/b")
	for _, preview := range []*sites.Preview{first, second} {
		label, ok := strings.CutSuffix(preview.Hostname, "."+testDomain)
		if !ok || !previewLabel.MatchString(label) {
			t.Fatalf("preview hostname %q is not <site>--<26 base32>.%s", preview.Hostname, testDomain)
		}
		if sites.ValidateName(label) == nil {
			t.Fatalf("preview label %q is also a valid site name; the two could collide", label)
		}
	}
	if first.Hostname == second.Hostname {
		t.Fatal("two previews share a hostname")
	}
}

func TestPreviewLabelKeepsWithinOneDNSLabelForLongSiteNames(t *testing.T) {
	h := newPreviewHarness(t, 5, 100)
	site := h.createSite(t, strings.Repeat("a", 29)+"-"+strings.Repeat("b", 33))
	preview := h.mustOpen(t, site, sites.PreviewSourcePullRequest, "12")
	label, _ := strings.CutSuffix(preview.Hostname, "."+testDomain)
	if len(label) > 63 || strings.Contains(strings.SplitN(label, "--", 2)[0], "-") && strings.HasSuffix(strings.SplitN(label, "--", 2)[0], "-") {
		t.Fatalf("label %q is not one clean DNS label", label)
	}
}

func TestOpenPreviewIsStablePerRef(t *testing.T) {
	h := newPreviewHarness(t, 5, 100)
	site := h.createSite(t, "shop")
	first := h.mustOpen(t, site, sites.PreviewSourcePullRequest, "12")
	again := h.mustOpen(t, site, sites.PreviewSourcePullRequest, "12")
	if first.ID != again.ID || first.Hostname != again.Hostname {
		t.Fatalf("reopening PR 12 made a new preview: %+v vs %+v", first, again)
	}
	other := h.mustOpen(t, site, sites.PreviewSourceBranch, "12")
	if other.ID == first.ID {
		t.Fatal("a branch named 12 shares the preview of PR 12")
	}
}

func TestOpenPreviewRefusesPastThePlanButReopensHeldOnes(t *testing.T) {
	h := newPreviewHarness(t, 1, 100)
	site := h.createSite(t, "shop")
	h.mustOpen(t, site, sites.PreviewSourceBranch, "feat/a")
	if _, err := h.open(t, site, sites.PreviewSourceBranch, "feat/b"); !errors.Is(err, sites.ErrPreviewLimitReached) {
		t.Fatalf("second preview on a plan of 1: err = %v, want ErrPreviewLimitReached", err)
	}
	if _, err := h.open(t, site, sites.PreviewSourceBranch, "feat/a"); err != nil {
		t.Fatalf("a held preview must still rebuild at the cap: %v", err)
	}
}

func TestOpenPreviewRefusesPastTheInstallCap(t *testing.T) {
	h := newPreviewHarness(t, 10, 1)
	site := h.createSite(t, "shop")
	h.mustOpen(t, site, sites.PreviewSourceBranch, "feat/a")
	if _, err := h.open(t, site, sites.PreviewSourceBranch, "feat/b"); !errors.Is(err, sites.ErrPreviewInstallFull) {
		t.Fatalf("err = %v, want ErrPreviewInstallFull", err)
	}
}

func TestOpenPreviewRefusesBadInput(t *testing.T) {
	h := newPreviewHarness(t, 5, 100)
	site := h.createSite(t, "shop")
	for _, request := range []sites.PreviewRequest{
		{Source: "tag", Ref: "v1"}, {Source: sites.PreviewSourceBranch, Ref: ""},
		{Source: sites.PreviewSourcePullRequest, Ref: "twelve"}, {Source: sites.PreviewSourceBranch, Ref: "a\nb"},
	} {
		request.ProjectID, request.SiteID = testProject, site.ID
		var input *sites.InputError
		if _, err := h.service.OpenPreview(context.Background(), request); !errors.As(err, &input) {
			t.Errorf("OpenPreview(%+v) err = %v, want an InputError", request, err)
		}
	}
	if _, err := h.service.OpenPreview(context.Background(), sites.PreviewRequest{ProjectID: "other", SiteID: site.ID,
		Source: sites.PreviewSourceBranch, Ref: "x"}); !errors.Is(err, sites.ErrSiteNotFound) {
		t.Fatalf("another project's site: err = %v", err)
	}
}

func TestDeployPreviewPublishesOnlyThePreviewWorkerAndHost(t *testing.T) {
	h := newPreviewHarness(t, 5, 100)
	site := h.createSite(t, "shop")
	preview := h.mustOpen(t, site, sites.PreviewSourceBranch, "feat/a")
	deployed, err := h.deployPreview(t, preview, false)
	if err != nil {
		t.Fatalf("DeployPreview: %v", err)
	}
	script := sites.PreviewScriptName(preview)
	if script != "site-pv-"+preview.ID {
		t.Fatalf("script = %q", script)
	}
	if _, ok := h.publisher.Published[script]; !ok || len(h.publisher.Published) != 1 {
		t.Fatalf("published = %v, want only %s", h.publisher.Published, script)
	}
	if h.publisher.Hosts[preview.Hostname] != script || h.publisher.Hosts[site.Hostname] != "" {
		t.Fatalf("hosts = %v", h.publisher.Hosts)
	}
	if len(h.origins.Origins) != 0 {
		t.Fatalf("a preview without API access added CORS origins %v", h.origins.Origins)
	}
	if deployed.Status != sites.PreviewReady || deployed.DeployedSHA != "abc1234def" || deployed.FileCount == 0 {
		t.Fatalf("preview after deploy = %+v", deployed)
	}
	live, _ := h.store.Get(context.Background(), testProject, site.ID)
	if live.LiveDeployID != "" {
		t.Fatal("a preview deploy changed the site's live deploy")
	}
}

func TestDeployPreviewWithAPIAccessOwnsItsOriginUntilRemoved(t *testing.T) {
	h := newPreviewHarness(t, 5, 100)
	site := h.createSite(t, "shop")
	preview := h.mustOpen(t, site, sites.PreviewSourcePullRequest, "7")
	if _, err := h.deployPreview(t, preview, true); err != nil {
		t.Fatal(err)
	}
	if len(h.origins.Origins) != 1 || h.origins.Origins[0] != "https://"+preview.Hostname {
		t.Fatalf("origins = %v", h.origins.Origins)
	}
	if err := h.service.RemovePreview(context.Background(), testProject, site.ID, preview.ID); err != nil {
		t.Fatal(err)
	}
	if len(h.origins.Origins) != 0 {
		t.Fatalf("origins after removal = %v", h.origins.Origins)
	}
}

func TestDeployPreviewFailureIsRecordedOnThePreview(t *testing.T) {
	h := newPreviewHarness(t, 5, 100)
	site := h.createSite(t, "shop")
	preview := h.mustOpen(t, site, sites.PreviewSourceBranch, "feat/a")
	h.publisher.FailPut = errors.New("cloudflare says no")
	if _, err := h.deployPreview(t, preview, false); err == nil {
		t.Fatal("want the publish failure")
	}
	stored, _ := h.service.GetPreview(context.Background(), testProject, site.ID, preview.ID)
	if stored.Status != sites.PreviewFailed || stored.FailureReason == "" || strings.Contains(stored.FailureReason, "cloudflare says no") {
		t.Fatalf("stored = %+v; want FAILED with the fixed message, not Cloudflare's", stored)
	}
}

func TestDeployPreviewOfARemovedPreviewIsRefused(t *testing.T) {
	h := newPreviewHarness(t, 5, 100)
	site := h.createSite(t, "shop")
	preview := h.mustOpen(t, site, sites.PreviewSourcePullRequest, "3")
	if err := h.service.RemovePreview(context.Background(), testProject, site.ID, preview.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.deployPreview(t, preview, false); !errors.Is(err, sites.ErrPreviewNotFound) {
		t.Fatalf("err = %v, want ErrPreviewNotFound", err)
	}
	if len(h.publisher.Published) != 0 {
		t.Fatal("a removed preview was published")
	}
}

func TestRemovePreviewUnroutesThenDeletesTheWorker(t *testing.T) {
	h := newPreviewHarness(t, 5, 100)
	site := h.createSite(t, "shop")
	preview := h.mustOpen(t, site, sites.PreviewSourceBranch, "feat/a")
	if _, err := h.deployPreview(t, preview, false); err != nil {
		t.Fatal(err)
	}
	h.publisher.Calls = nil
	if err := h.service.RemovePreviewByRef(context.Background(), testProject, site.ID, sites.PreviewSourceBranch, "feat/a"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(h.publisher.Calls, ",") != "delete-host,delete-script" {
		t.Fatalf("calls = %v", h.publisher.Calls)
	}
	if len(h.publisher.Hosts) != 0 || h.publisher.Deleted[0] != sites.PreviewScriptName(preview) {
		t.Fatalf("hosts = %v deleted = %v", h.publisher.Hosts, h.publisher.Deleted)
	}
	if got, _ := h.service.ListPreviews(context.Background(), testProject, site.ID); len(got) != 0 {
		t.Fatalf("previews after removal = %v", got)
	}
	if err := h.service.RemovePreviewByRef(context.Background(), testProject, site.ID, sites.PreviewSourceBranch, "feat/a"); err != nil {
		t.Fatalf("removing again must be a no-op: %v", err)
	}
}

func TestFailedRemovalIsRetriedByTheSweep(t *testing.T) {
	h := newPreviewHarness(t, 5, 100)
	site := h.createSite(t, "shop")
	preview := h.mustOpen(t, site, sites.PreviewSourceBranch, "feat/a")
	h.publisher.FailKV = errors.New("kv down")
	if err := h.service.RemovePreview(context.Background(), testProject, site.ID, preview.ID); err == nil {
		t.Fatal("want the removal to fail")
	}
	stored, _ := h.service.GetPreview(context.Background(), testProject, site.ID, preview.ID)
	if stored == nil || stored.Status != sites.PreviewRemoving {
		t.Fatalf("stored = %+v, want REMOVING", stored)
	}
	if _, err := h.open(t, site, sites.PreviewSourceBranch, "feat/a"); !errors.Is(err, sites.ErrPreviewRemoving) {
		t.Fatalf("reopening a preview being removed: err = %v", err)
	}
	h.publisher.FailKV = nil
	report := h.service.SweepPreviews(context.Background())
	if report.Removed != 1 || report.Failed != 0 {
		t.Fatalf("report = %+v", report)
	}
	if got, _ := h.service.GetPreview(context.Background(), testProject, site.ID, preview.ID); got != nil {
		t.Fatalf("preview still stored: %+v", got)
	}
}

func TestSweepRemovesPreviewsIdlePastTheirLifetime(t *testing.T) {
	h := newPreviewHarness(t, 5, 100)
	site := h.createSite(t, "shop")
	old := h.mustOpen(t, site, sites.PreviewSourceBranch, "old")
	h.now = h.now.Add(10 * 24 * time.Hour)
	fresh := h.mustOpen(t, site, sites.PreviewSourceBranch, "fresh")
	h.now = h.now.Add(5 * 24 * time.Hour)
	report := h.service.SweepPreviews(context.Background())
	if report.Removed != 1 {
		t.Fatalf("report = %+v", report)
	}
	if got, _ := h.service.GetPreview(context.Background(), testProject, site.ID, old.ID); got != nil {
		t.Fatal("the idle preview was kept")
	}
	if got, _ := h.service.GetPreview(context.Background(), testProject, site.ID, fresh.ID); got == nil {
		t.Fatal("the fresh preview was removed")
	}
}

func TestDeletingTheSiteRemovesItsPreviewsFirst(t *testing.T) {
	h := newPreviewHarness(t, 5, 100)
	site := h.createSite(t, "shop")
	preview := h.mustOpen(t, site, sites.PreviewSourceBranch, "feat/a")
	if _, err := h.deployPreview(t, preview, false); err != nil {
		t.Fatal(err)
	}
	if err := h.service.Delete(context.Background(), testProject, site.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.publisher.Published[sites.PreviewScriptName(preview)]; ok {
		t.Fatal("the preview's Worker outlived its site")
	}
	if len(h.publisher.Hosts) != 0 {
		t.Fatalf("hosts = %v", h.publisher.Hosts)
	}
}

func TestDeletingTheSiteKeepsItWhenAPreviewCannotBeRemoved(t *testing.T) {
	h := newPreviewHarness(t, 5, 100)
	site := h.createSite(t, "shop")
	h.mustOpen(t, site, sites.PreviewSourceBranch, "feat/a")
	h.publisher.FailKV = errors.New("kv down")
	if err := h.service.Delete(context.Background(), testProject, site.ID); err == nil {
		t.Fatal("want the deletion to fail while a preview is left on Cloudflare")
	}
	if got, _ := h.store.Get(context.Background(), testProject, site.ID); got == nil {
		t.Fatal("the site row went while its preview was left behind")
	}
}

func TestPreviewUsageCountsTheProject(t *testing.T) {
	h := newPreviewHarness(t, 4, 100)
	first, second := h.createSite(t, "shop"), h.createSite(t, "notes")
	h.mustOpen(t, first, sites.PreviewSourceBranch, "a")
	h.mustOpen(t, second, sites.PreviewSourceBranch, "b")
	used, maximum, err := h.service.PreviewUsage(context.Background(), testProject)
	if err != nil || used != 2 || maximum != 4 {
		t.Fatalf("usage = %d of %d, %v", used, maximum, err)
	}
}

func TestPreviewsAreUnavailableWithoutAStore(t *testing.T) {
	h := newHarness(t)
	site := h.createSite(t, "shop")
	if h.service.PreviewsAvailable() {
		t.Fatal("previews offered without a store")
	}
	_, err := h.service.OpenPreview(context.Background(), sites.PreviewRequest{ProjectID: testProject, SiteID: site.ID,
		Source: sites.PreviewSourceBranch, Ref: "a"})
	if !errors.Is(err, sites.ErrPreviewsUnavailable) {
		t.Fatalf("err = %v", err)
	}
}
