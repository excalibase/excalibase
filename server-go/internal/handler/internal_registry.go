package handler

import (
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// PullRotator moves a project to a new internal-registry pull password (EXC-540).
type PullRotator interface {
	RotatePull(projectID string) (int, error)
}

// InternalRegistryHandler retires a project's pull credential, as after a leak.
type InternalRegistryHandler struct {
	rotator PullRotator
}

func NewInternalRegistryHandler(rotator PullRotator) *InternalRegistryHandler {
	return &InternalRegistryHandler{rotator: rotator}
}

func (h *InternalRegistryHandler) Routes(r chi.Router) {
	r.Post("/pull-credential/rotate", h.Rotate)
}

func (h *InternalRegistryHandler) Rotate(w http.ResponseWriter, r *http.Request) {
	if h.rotator == nil {
		http.NotFound(w, r)
		return
	}
	projectID := chi.URLParam(r, "projectId")
	generation, err := h.rotator.RotatePull(projectID)
	if err != nil {
		log.Printf("internal registry: rotate %s's pull credential: %v", projectID, err)
		httpError(w, "the pull credential could not be rotated; try again", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, map[string]any{
		"generation": generation,
		"message":    "the old pull credential no longer works; each app pulls with the new one from its next deploy",
	})
}
