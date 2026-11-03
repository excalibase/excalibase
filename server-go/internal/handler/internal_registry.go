package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/service"
)

// PullRotator moves a project to a new internal-registry pull password and
// re-renders its running apps' pull secrets (EXC-540).
type PullRotator interface {
	RotatePull(ctx context.Context, projectID string) (int, error)
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
	generation, err := h.rotator.RotatePull(r.Context(), projectID)
	if err != nil {
		log.Printf("internal registry: rotate %s's pull credential: %v", projectID, err)
		httpError(w, rotateFailure(err), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, map[string]any{
		"generation": generation,
		"message":    "the old pull credential no longer works; every app's pull secret now holds the new one, no redeploy needed",
	})
}

// rotateFailure names the apps a rotation left on the retired password; their next image pull fails until fixed.
func rotateFailure(err error) string {
	var stale *service.StalePullSecretsError
	if !errors.As(err, &stale) {
		return "the pull credential could not be rotated; try again"
	}
	if len(stale.Apps) == 0 {
		return "the pull credential was rotated, but the apps' pull secrets could not be updated; rotate again"
	}
	return "the pull credential was rotated, but these apps still hold the old one and cannot pull their image until you rotate again: " +
		strings.Join(stale.Apps, ", ")
}
