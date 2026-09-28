package handler

import (
	"encoding/json"
	"errors"
	"github.com/excalibase/provisioning-poc/internal/storagebudget"
	"log"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

// errClusterChangeFailed is all a caller learns when a change fails on the
// platform's side; the cause is in the log.
const errClusterChangeFailed = "the change could not be applied; try again later"

// GetClusterSettings answers the project's disk, size, plan and Postgres
// settings, and which settings may be tuned (EXC-492).
func (h *ProvisioningHandler) GetClusterSettings(w http.ResponseWriter, r *http.Request) {
	h.writeClusterSettings(w, r, chi.URLParam(r, "projectId"))
}

// ResizeStorage grows the project's disk. Body: {"size": "10Gi"}.
func (h *ProvisioningHandler) ResizeStorage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Size string `json:"size"`
	}
	if !decodeStrict(w, r, &body) {
		return
	}
	projectID := chi.URLParam(r, "projectId")
	if err := h.svc.ResizeStorage(r.Context(), projectID, body.Size); err != nil {
		writeClusterChangeError(w, projectID, "disk resize", err)
		return
	}
	h.writeClusterSettings(w, r, projectID)
}

// ChangeTier moves the project onto its organization's plan. Body:
// {"tier": "STANDARD"} — the plan the caller expects the org to be on; any
// other is refused, because a project's tier follows its org (EXC-470).
func (h *ProvisioningHandler) ChangeTier(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Tier domain.TierType `json:"tier"`
	}
	if !decodeStrict(w, r, &body) {
		return
	}
	if !domain.IsValidTier(body.Tier) {
		httpError(w, "unknown tier", http.StatusBadRequest)
		return
	}
	projectID := chi.URLParam(r, "projectId")
	if err := h.svc.ApplyOrgTier(r.Context(), projectID, body.Tier); err != nil {
		writeClusterChangeError(w, projectID, "tier change", err)
		return
	}
	h.writeClusterSettings(w, r, projectID)
}

// TuneParameters sets the project's tunable Postgres settings to exactly the
// given set. Body: {"parameters": {"work_mem": "8MB"}}; {} clears them.
func (h *ProvisioningHandler) TuneParameters(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Parameters map[string]string `json:"parameters"`
	}
	if !decodeStrict(w, r, &body) {
		return
	}
	if body.Parameters == nil {
		httpError(w, "parameters is required; send {} to restore the defaults", http.StatusBadRequest)
		return
	}
	projectID := chi.URLParam(r, "projectId")
	if err := h.svc.TuneParameters(r.Context(), projectID, body.Parameters); err != nil {
		writeClusterChangeError(w, projectID, "parameter change", err)
		return
	}
	h.writeClusterSettings(w, r, projectID)
}

func (h *ProvisioningHandler) writeClusterSettings(w http.ResponseWriter, r *http.Request, projectID string) {
	settings, err := h.svc.ClusterSettings(r.Context(), projectID)
	if err != nil {
		writeClusterChangeError(w, projectID, "cluster settings read", err)
		return
	}
	access := custommw.ProjectAccessFromContext(r.Context())
	writeJSON(w, struct {
		*service.ClusterSettings
		CanChange bool `json:"canChange"`
	}{settings, access != nil && access.RoleAtLeast(domain.OrgRoleAdmin)})
}

func decodeStrict(w http.ResponseWriter, r *http.Request, into interface{}) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return false
	}
	return true
}

// writeClusterChangeError answers a refusal with its reason and a platform
// failure with nothing but that it failed.
func writeClusterChangeError(w http.ResponseWriter, projectID, what string, err error) {
	status := clusterChangeStatus(err)
	if status == http.StatusInternalServerError || status == http.StatusServiceUnavailable {
		log.Printf("ERROR: %s for project %s: %v", what, projectID, err)
		httpError(w, errClusterChangeFailed, status)
		return
	}
	if errors.Is(err, k8s.ErrVolumeExpansionUnsupported) {
		// Which volume and storage class are the platform's business.
		log.Printf("INFO: %s refused for project %s: %v", what, projectID, err)
		httpError(w, k8s.ErrVolumeExpansionUnsupported.Error()+" on this platform", status)
		return
	}
	httpError(w, safeError(err), status)
}

func clusterChangeStatus(err error) int {
	var nodes *service.NotEnoughNodesError
	switch {
	case errors.Is(err, service.ErrInvalidStorageSize), errors.Is(err, service.ErrStorageShrink),
		errors.Is(err, config.ErrTenantParameter), errors.Is(err, service.ErrKubernetesOnly):
		return http.StatusBadRequest
	case errors.Is(err, service.ErrStorageAbovePlan), errors.Is(err, service.ErrTierNotOrgPlan),
		errors.Is(err, service.ErrDiskAbovePlanMax), errors.Is(err, service.ErrTierParametersOutOfBounds),
		errors.Is(err, service.ErrPlanDoesNotFit), errors.Is(err, storagebudget.ErrExceeded),
		errors.Is(err, k8s.ErrVolumeExpansionUnsupported), errors.As(err, &nodes):
		return http.StatusConflict
	case errors.Is(err, service.ErrNodePlacementUnknown):
		return http.StatusServiceUnavailable
	}
	return unsettledOr(err, http.StatusInternalServerError)
}
