package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// An admin's MFA reset for one end user (EXC-662 step 7). The caller mounts
// it behind the authmfa flag, so it is unmounted while the flag is off.

const auditActionEndUserFactorsReset = "end_user.factors.reset"

// FactorRoutes mounts DELETE /{userId}/factors.
func (h *EndUsersHandler) FactorRoutes(r chi.Router) {
	r.Delete("/{userId}/factors", h.ResetFactors)
}

func (h *EndUsersHandler) ResetFactors(w http.ResponseWriter, r *http.Request) {
	target, userID, ok := h.resolveUser(w, r)
	if !ok {
		return
	}
	err := h.users.ResetFactors(r.Context(), target.orgSlug, target.projectID, target.actor, userID)
	h.record(r, target, auditActionEndUserFactorsReset, withOutcome(map[string]any{"userId": userID}, err))
	if err != nil {
		writeEndUserError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
