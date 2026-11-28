package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/go-chi/chi/v5"
)

type fakeAnalytics struct {
	days int
	err  error
}

func (f *fakeAnalytics) UserAnalytics(_ context.Context, days int) (*adminconsole.UserAnalytics, error) {
	f.days = days
	return &adminconsole.UserAnalytics{WindowDays: days, Signups: 7}, f.err
}
func (f *fakeAnalytics) AbuseAnalytics(_ context.Context, days int) (*adminconsole.AbuseAnalytics, error) {
	f.days = days
	return &adminconsole.AbuseAnalytics{WindowDays: days, RefusedStillActive: 3}, f.err
}
func (f *fakeAnalytics) TrafficAnalytics(_ context.Context, days int) (*adminconsole.TrafficAnalytics, error) {
	f.days = days
	return &adminconsole.TrafficAnalytics{WindowDays: days, PeakConnections: 35}, f.err
}

func (f *fakeAnalytics) CreditAnalytics(_ context.Context, days int) (*adminconsole.CreditAnalytics, error) {
	f.days = days
	return &adminconsole.CreditAnalytics{OrgsAtZero: 4}, f.err
}

var analyticsPaths = []string{"/api/admin/analytics/users", "/api/admin/analytics/abuse", "/api/admin/analytics/traffic", "/api/admin/analytics/credits"}

func analyticsRouter(role, scopes string, flags features.Flags, reads adminconsole.AnalyticsReader) http.Handler {
	h := NewAdminAnalyticsHandler(reads, flags)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.SetUser(req.Context(), &domain.User{ID: "boss", Role: role, Active: true})
			ctx = auth.SetToken(ctx, &domain.AccessToken{Scopes: scopes})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Route("/api/admin/analytics", func(r chi.Router) { h.Routes(r) })
	return r
}

func analyticsGet(router http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, strings.NewReader("")))
	return w
}

func TestAdminAnalytics_OnlyAPlatformAdminSessionMayReadAndOthersMeetAMissingRoute(t *testing.T) {
	on := features.NewStatic(features.AdminAnalytics)
	for _, role := range []string{"platform_operator", "platform_viewer", "user"} {
		router := analyticsRouter(role, auth.ScopeSession, on, &fakeAnalytics{})
		for _, path := range analyticsPaths {
			if w := analyticsGet(router, path); w.Code != http.StatusNotFound {
				t.Errorf("%s as %s = %d, want 404 like a missing route", path, role, w.Code)
			}
		}
	}
	token := analyticsRouter("platform_admin", auth.ScopeRead, on, &fakeAnalytics{})
	for _, path := range analyticsPaths {
		if w := analyticsGet(token, path); w.Code != http.StatusNotFound {
			t.Errorf("%s with an access token = %d, want 404 like a missing route", path, w.Code)
		}
	}
	admin := analyticsRouter("platform_admin", auth.ScopeSession, on, &fakeAnalytics{})
	for _, path := range analyticsPaths {
		if w := analyticsGet(admin, path); w.Code != http.StatusOK {
			t.Errorf("%s as platform_admin = %d: %s", path, w.Code, w.Body.String())
		}
	}
}

func TestAdminAnalytics_FlagOffAnswersLikeAnUnmountedRoute(t *testing.T) {
	reads := &fakeAnalytics{}
	router := analyticsRouter("platform_admin", auth.ScopeSession, features.NewStatic(), reads)
	for _, path := range analyticsPaths {
		if w := analyticsGet(router, path); w.Code != http.StatusNotFound {
			t.Errorf("%s with the flag off = %d, want 404", path, w.Code)
		}
	}
	if reads.days != 0 {
		t.Fatalf("a disabled route must not read the database, asked for %d days", reads.days)
	}
}

func TestAdminAnalytics_ReadsAreBoundedByTheWindow(t *testing.T) {
	reads := &fakeAnalytics{}
	router := analyticsRouter("platform_admin", auth.ScopeSession, features.NewStatic(features.AdminAnalytics), reads)
	analyticsGet(router, "/api/admin/analytics/users?days=9999")
	if reads.days != adminconsole.AnalyticsWindowDays {
		t.Fatalf("days = %d, want the fixed %d-day window whatever the query says", reads.days, adminconsole.AnalyticsWindowDays)
	}
}

func TestAdminAnalytics_TrafficLooksBackAsFarAsMetricsAreKept(t *testing.T) {
	reads := &fakeAnalytics{}
	router := analyticsRouter("platform_admin", auth.ScopeSession, features.NewStatic(features.AdminAnalytics), reads)
	analyticsGet(router, "/api/admin/analytics/traffic")
	if reads.days != adminconsole.TrafficWindowDays || adminconsole.TrafficWindowDays != 7 {
		t.Fatalf("traffic days = %d, want the 7 days the sampler keeps", reads.days)
	}
}

func TestAdminAnalytics_PermissionIsCheckedBeforeTheFlag(t *testing.T) {
	for _, role := range []string{"platform_operator", "platform_viewer", "user"} {
		router := analyticsRouter(role, auth.ScopeSession, features.NewStatic(), &fakeAnalytics{})
		for _, path := range analyticsPaths {
			off := analyticsGet(router, path)
			on := analyticsGet(analyticsRouter(role, auth.ScopeSession, features.NewStatic(features.AdminAnalytics), &fakeAnalytics{}), path)
			if off.Code != http.StatusNotFound || off.Code != on.Code || off.Body.String() != on.Body.String() {
				t.Errorf("%s as %s: flag off %d %q, flag on %d %q; non-admins must not be told whether the feature exists", path, role, off.Code, off.Body.String(), on.Code, on.Body.String())
			}
		}
	}
}

func TestAdminAnalytics_AFailedReadIsABare500(t *testing.T) {
	router := analyticsRouter("platform_admin", auth.ScopeSession, features.NewStatic(features.AdminAnalytics), &fakeAnalytics{err: errors.New("pq: relation secret_table")})
	w := analyticsGet(router, "/api/admin/analytics/abuse")
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "secret_table") {
		t.Fatalf("got %d %s, want a 500 that leaks nothing", w.Code, w.Body.String())
	}
}

func TestAdminAnalytics_CreditsServesTheOrgsAtZero(t *testing.T) {
	router := analyticsRouter("platform_admin", auth.ScopeSession, features.NewStatic(features.AdminAnalytics), &fakeAnalytics{})
	w := analyticsGet(router, "/api/admin/analytics/credits")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"orgsAtZero":4`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
