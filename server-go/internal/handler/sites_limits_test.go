package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

type planLimits struct{ limits sites.Limits }

func (p planLimits) DeployLimits(context.Context, string) (sites.Limits, error) { return p.limits, nil }

func newLimitedSitesHarness(t *testing.T, limits sites.Limits) *sitesHarness {
	t.Helper()
	publisher, origins := sitestest.NewPublisher(), sitestest.NewOrigins()
	service, err := sites.NewService(sitestest.NewMemoryStore(), publisher, origins, sites.Options{BaseDomain: "excalibase.app",
		SpoolDir: t.TempDir(), DeployLimits: planLimits{limits}})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return &sitesHarness{router: sitesRouter(NewSiteHandler(service)), publisher: publisher, origins: origins}
}

// Studio shows the plan's limits on the Sites page (EXC-620).
func TestSiteLimitsAnswerThePlan(t *testing.T) {
	h := newLimitedSitesHarness(t, sites.Limits{MaxSites: 20, MaxTotalBytes: 100 << 20, MaxFiles: 20_000, MaxFileBytes: 25 << 20, MaxDeploysPerHour: 120, MaxCustomDomains: 5})
	response := h.do(t, http.MethodGet, "/limits", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("limits = %d %s", response.Code, response.Body)
	}
	for _, field := range []string{`"maxSites":20`, `"maxDeployBytes":104857600`, `"maxFiles":20000`, `"maxFileBytes":26214400`, `"maxDeploysPerHour":120`, `"maxCustomDomains":5`} {
		if !strings.Contains(response.Body.String(), field) {
			t.Errorf("limits %s lack %s", response.Body, field)
		}
	}
}

// A deploy past the plan's hourly rate is 429 with the seconds to wait.
func TestSiteDeployPastTheHourlyRateIsTooManyRequests(t *testing.T) {
	limits := sites.DefaultLimits
	limits.MaxDeploysPerHour = 1
	h := newLimitedSitesHarness(t, limits)
	site := h.createSite(t, "shop")
	archive := sitestest.Dist(t, "<!doctype html>")
	if first := h.do(t, http.MethodPost, "/"+site.ID+"/deploy", archive); first.Code != http.StatusCreated {
		t.Fatalf("first deploy = %d %s", first.Code, first.Body)
	}
	second := h.do(t, http.MethodPost, "/"+site.ID+"/deploy", archive)
	if second.Code != http.StatusTooManyRequests || !strings.Contains(second.Body.String(), "deploys this hour") {
		t.Fatalf("second deploy = %d %s, want 429", second.Code, second.Body)
	}
	wait, err := strconv.Atoi(second.Header().Get("Retry-After"))
	if err != nil || wait < 3500 || wait > int(time.Hour/time.Second) {
		t.Fatalf("Retry-After = %q, want the seconds until the first deploy leaves the hour", second.Header().Get("Retry-After"))
	}
}

func TestSiteRateRefusalRoundsRetryAfterUp(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeSiteError(recorder, &sites.RateLimitError{RetryAfter: 1500 * time.Millisecond})
	if recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("Retry-After") != "2" {
		t.Fatalf("got %d Retry-After %q, want 429 and 2", recorder.Code, recorder.Header().Get("Retry-After"))
	}
}
