package handler

import (
	"context"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/go-chi/chi/v5"
)

// AdminAttentionHandler serves the lists of the console's "needs attention"
// card a page at a time. It has the console's gate: manage_orgs and a signed-in
// session, since the rows name accounts by address.
type AdminAttentionHandler struct {
	attention *adminconsole.Attention
}

func NewAdminAttentionHandler(attention *adminconsole.Attention) *AdminAttentionHandler {
	return &AdminAttentionHandler{attention: attention}
}

// Routes mounts under /api/admin/attention; the caller has authenticated the request.
func (h *AdminAttentionHandler) Routes(r chi.Router) {
	r.Use(auth.RequirePermission(auth.PermManageOrgs))
	r.Use(requireSession)
	r.Get("/suspensions", attentionRead(h.attention.Suspensions, "attention suspensions"))
	r.Get("/failed-projects", attentionRead(h.attention.FailedProjects, "attention failed projects"))
}

func attentionRead[T any](read func(ctx context.Context, cursor string, limit int) (adminconsole.CursorPage[T], error), what string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, err := optionalInt(r.URL.Query().Get("limit"))
		if err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}
		page, err := read(r.Context(), r.URL.Query().Get("cursor"), limit)
		if err != nil {
			consoleFailure(w, what, err)
			return
		}
		writeJSON(w, page)
	}
}
