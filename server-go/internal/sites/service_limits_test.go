package sites_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

type limitsHarness struct {
	service   *sites.Service
	store     *sitestest.MemoryStore
	publisher *sitestest.Publisher
	clock     *time.Time
}

func newLimitsHarness(t *testing.T, limits sites.DeployLimiter) *limitsHarness {
	t.Helper()
	store, publisher := sitestest.NewMemoryStore(), sitestest.NewPublisher()
	service, err := sites.NewService(store, publisher, sitestest.NewOrigins(),
		sites.Options{BaseDomain: testDomain, SpoolDir: t.TempDir(), DeployLimits: limits})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	clock := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	service.SetNow(func() time.Time { return clock })
	store.Now = func() time.Time { return clock }
	return &limitsHarness{service: service, store: store, publisher: publisher, clock: &clock}
}

func (h *limitsHarness) deploy(t *testing.T, site *sites.Site) (*sites.Deploy, error) {
	t.Helper()
	return h.service.Deploy(context.Background(), sites.DeployRequest{
		ProjectID: testProject, SiteID: site.ID, Actor: testActor, Body: bytes.NewReader(viteDist(t)),
	})
}

type failingLimits struct{ err error }

func (f failingLimits) DeployLimits(context.Context, string) (sites.Limits, error) {
	return sites.Limits{}, f.err
}

// The plan says how many sites a project holds (EXC-620), not a constant.
func TestCreateHoldsTheProjectToItsPlansSiteCount(t *testing.T) {
	limits := sites.DefaultLimits
	limits.MaxSites = 2
	h := newLimitsHarness(t, fixedLimits{limits})
	for _, name := range []string{"one", "two"} {
		if _, err := h.service.Create(context.Background(), testProject, name, nil, testActor); err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
	}
	if _, err := h.service.Create(context.Background(), testProject, "three", nil, testActor); !errors.Is(err, sites.ErrSiteLimitReached) {
		t.Fatalf("third site: err = %v, want the plan's limit", err)
	}
	none := newLimitsHarness(t, fixedLimits{sites.Limits{MaxSites: 0}})
	if _, err := none.service.Create(context.Background(), testProject, "one", nil, testActor); !errors.Is(err, sites.ErrSiteLimitReached) {
		t.Fatalf("a plan offering no sites: err = %v", err)
	}
}

// A plan that cannot be read refuses the create rather than guessing a limit.
func TestCreateRefusesWhenThePlanCannotBeRead(t *testing.T) {
	broken := errors.New("organisation not found")
	h := newLimitsHarness(t, failingLimits{broken})
	if _, err := h.service.Create(context.Background(), testProject, "one", nil, testActor); !errors.Is(err, broken) {
		t.Fatalf("err = %v, want the plan's read failure", err)
	}
	if len(h.store.Sites) != 0 {
		t.Fatal("a site was created without a limit")
	}
}

// Deploys per hour per project (ADR 0043 decision 11): the one past the plan's
// rate is refused before its upload is read, and says when the next may start.
func TestDeployIsRateLimitedPerProjectPerHour(t *testing.T) {
	limits := sites.DefaultLimits
	limits.MaxDeploysPerHour = 2
	h := newLimitsHarness(t, fixedLimits{limits})
	site, err := h.service.Create(context.Background(), testProject, "shop", nil, testActor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := h.deploy(t, site); err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	*h.clock = h.clock.Add(20 * time.Minute)
	if _, err := h.deploy(t, site); err != nil {
		t.Fatalf("second deploy: %v", err)
	}
	*h.clock = h.clock.Add(10 * time.Minute)
	sessions := h.publisher.Sessions

	_, err = h.deploy(t, site)
	var limited *sites.RateLimitError
	if !errors.As(err, &limited) || !errors.Is(err, sites.ErrDeployRateLimited) {
		t.Fatalf("third deploy: err = %v, want the hourly rate", err)
	}
	// The first deploy leaves the hour 30 minutes from now.
	if limited.RetryAfter != 30*time.Minute {
		t.Errorf("RetryAfter = %v, want 30m", limited.RetryAfter)
	}
	if h.publisher.Sessions != sessions || len(h.store.Deploys) != 2 {
		t.Fatal("a refused deploy reached the CDN or the history")
	}

	*h.clock = h.clock.Add(30 * time.Minute)
	if _, err := h.deploy(t, site); err != nil {
		t.Fatalf("once the first left the hour: %v", err)
	}
}

// The rate is the project's, across all of its sites.
func TestDeployRateCountsEverySiteOfTheProject(t *testing.T) {
	limits := sites.DefaultLimits
	limits.MaxDeploysPerHour = 1
	h := newLimitsHarness(t, fixedLimits{limits})
	shop, _ := h.service.Create(context.Background(), testProject, "shop", nil, testActor)
	journal, _ := h.service.Create(context.Background(), testProject, "journal", nil, testActor)
	if _, err := h.deploy(t, shop); err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	if _, err := h.deploy(t, journal); !errors.Is(err, sites.ErrDeployRateLimited) {
		t.Fatalf("another site of the project: err = %v, want the project's rate", err)
	}
}

// Studio and CI read the project's limits to say what a deploy may hold.
func TestLimitsAnswerTheProjectsPlan(t *testing.T) {
	limits := sites.Limits{MaxFiles: 10, MaxFileBytes: 1 << 20, MaxTotalBytes: 2 << 20, MaxSites: 3, MaxDeploysPerHour: 4}
	h := newLimitsHarness(t, fixedLimits{limits})
	got, err := h.service.Limits(context.Background(), testProject)
	if err != nil || got != limits {
		t.Fatalf("Limits = %+v, %v; want %+v", got, err, limits)
	}
}

// A refused archive still counts: a leaked token cannot upload without end.
func TestDeployRateCountsUploadsThatAreRefused(t *testing.T) {
	limits := sites.DefaultLimits
	limits.MaxDeploysPerHour = 1
	h := newLimitsHarness(t, fixedLimits{limits})
	site, _ := h.service.Create(context.Background(), testProject, "shop", nil, testActor)
	_, err := h.service.Deploy(context.Background(), sites.DeployRequest{ProjectID: testProject, SiteID: site.ID,
		Body: bytes.NewReader([]byte("not an archive"))})
	var archiveErr *sites.ArchiveError
	if !errors.As(err, &archiveErr) {
		t.Fatalf("first upload: err = %v, want the archive refused", err)
	}
	if _, err := h.deploy(t, site); !errors.Is(err, sites.ErrDeployRateLimited) {
		t.Fatalf("second upload: err = %v, want the hourly rate", err)
	}
}
