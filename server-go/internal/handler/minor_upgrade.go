package handler

import (
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
)

// statusActive is the settled state a project must be in before a rolling
// restart is started on it.
const statusActive = "ACTIVE"

// UpgradeMinorVersion moves a project onto the newest patch release of the
// major it already runs.
//
// The major is read off the project and is never taken from the request. A
// minor upgrade that could name a version would be a major upgrade wearing a
// smaller word, and a major upgrade is a different operation with different
// risks. Re-applying the version the project already records resolves to
// whatever the platform currently serves for that major, which is exactly
// "the newest patch".
//
// Like pause and resume, the answer is the project as the platform now holds
// it, read back from the store. The rolling restart the patch triggers is
// asynchronous, so a client that repainted from an assumed outcome would be
// claiming a state nobody has observed.
func (h *ProvisioningHandler) UpgradeMinorVersion(w http.ResponseWriter, r *http.Request) {
	if h.instances == nil {
		httpError(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	projectID := chi.URLParam(r, "projectId")
	inst, err := h.instances.FindByProjectID(projectID)
	if err != nil || inst == nil {
		httpError(w, "project not found", http.StatusNotFound)
		return
	}

	// Only the Kubernetes path has a CNPG Cluster to re-pin. Answering 200 for
	// a docker project would report an upgrade that never happened.
	if inst.DeploymentMode != "" && inst.DeploymentMode != domain.ModeK8s {
		httpError(w, "minor upgrades are available on Kubernetes projects only", http.StatusBadRequest)
		return
	}
	// A project mid-provision, paused or failing is not in a state a rolling
	// restart should be started on; the same call converges once it settles.
	if inst.Status != statusActive {
		httpError(w, "project is "+inst.Status+"; a minor upgrade needs an active project", http.StatusConflict)
		return
	}
	// Without a recorded major there is nothing to resolve the newest patch
	// of, and picking one would put bits under a tenant's data that nobody
	// chose.
	if inst.PostgresVersion == "" {
		httpError(w, "project records no PostgreSQL major, so its newest patch cannot be resolved", http.StatusConflict)
		return
	}

	if err := h.svc.UpgradeVersion(r.Context(), projectID, inst.PostgresVersion); err != nil {
		httpError(w, safeError(err), unsettledOr(err, http.StatusInternalServerError))
		return
	}

	got, err := h.instances.FindByProjectID(projectID)
	if err != nil || got == nil {
		httpError(w, "project not found", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]interface{}{
		"projectId":       got.ProjectID,
		"status":          got.Status,
		"currentStage":    got.CurrentStage,
		"postgresVersion": got.PostgresVersion,
	})
}
