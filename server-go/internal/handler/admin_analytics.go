package handler

import (
	"context"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/go-chi/chi/v5"
)

// AdminAnalyticsHandler serves the read-only numbers behind the admin console's
// analytics tabs (EXC-898). Like the console it needs manage_orgs and a
// signed-in session: the abuse list names accounts by address.
type AdminAnalyticsHandler struct {
	reads adminconsole.AnalyticsReader
	flags features.Flags
}

func NewAdminAnalyticsHandler(reads adminconsole.AnalyticsReader, flags features.Flags) *AdminAnalyticsHandler {
	return &AdminAnalyticsHandler{reads: reads, flags: flags}
}

// Routes mounts under /api/admin/analytics; the caller has authenticated the
// request. The flag is checked after the permission, so a caller who is not
// staff learns nothing about whether the feature exists.
func (h *AdminAnalyticsHandler) Routes(r chi.Router) {
	r.Use(auth.RequirePermission(auth.PermManageOrgs))
	r.Use(requireSession)
	r.Use(features.Require(h.flags, features.AdminAnalytics))
	r.Get("/users", analyticsRead(h.reads.UserAnalytics, adminconsole.AnalyticsWindowDays, "analytics users"))
	r.Get("/abuse", analyticsRead(h.reads.AbuseAnalytics, adminconsole.AnalyticsWindowDays, "analytics abuse"))
	r.Get("/traffic", analyticsRead(h.reads.TrafficAnalytics, adminconsole.TrafficWindowDays, "analytics traffic"))
}

// analyticsRead serves one read over a fixed window. The window is not a
// query parameter: a caller cannot ask the platform database for more.
func analyticsRead[T any](read func(ctx context.Context, days int) (*T, error), days int, what string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result, err := read(r.Context(), days)
		if err != nil {
			consoleFailure(w, what, err)
			return
		}
		writeJSON(w, result)
	}
}
