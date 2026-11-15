package handler

import (
	"encoding/json"
	"fmt"
	"strings"

	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

const maxTierReasonLength = 500

// SetAudit wires the log a platform plan change is written to.
func (h *OrgHandler) SetAudit(audit auditWriter) { h.audit = audit }

// ConsoleRoutes mounts the one cross-org org action the operator console
// owns: changing an org's plan, with a written reason (EXC-757). Org-by-id
// routes stay members-only; this is how the platform sets a plan on an org it
// does not belong to.
func (h *OrgHandler) ConsoleRoutes(r chi.Router) {
	r.With(auth.RequirePermission(auth.PermManageOrgs), requireSession).Patch("/console/orgs/{orgId}/tier", h.SetOrgTier)
}

// SetOrgTier changes an org's plan. The reason is required and audited.
func (h *OrgHandler) SetOrgTier(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tier   domain.TierType `json:"tier"`
		Reason string          `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		httpError(w, errInvalidRequest, http.StatusBadRequest)
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" || len(reason) > maxTierReasonLength {
		httpError(w, "a written reason of 1 to 500 characters is required", http.StatusBadRequest)
		return
	}
	if !domain.IsValidTier(req.Tier) {
		httpError(w, "invalid tier: must be FREE, STANDARD, or ENTERPRISE", http.StatusBadRequest)
		return
	}
	orgID := chi.URLParam(r, "orgId")
	org, err := h.orgStore.FindOrgByID(r.Context(), orgID)
	if err != nil || org == nil {
		httpError(w, errOrgNotFound, http.StatusNotFound)
		return
	}
	if !h.planFitsThePlatform(w, r, req.Tier) {
		return
	}
	previous := org.Tier
	org.Tier = req.Tier
	if err := h.orgStore.UpdateOrg(r.Context(), org); err != nil {
		httpError(w, "failed to update org", http.StatusInternalServerError)
		return
	}
	if h.planChanged != nil {
		h.planChanged()
	}
	h.recordTierChange(r, org, previous, reason)
	writeJSON(w, org)
}

func (h *OrgHandler) recordTierChange(r *http.Request, org *domain.Org, previous domain.TierType, reason string) {
	if h.audit == nil {
		return
	}
	actor := ""
	if user := auth.GetUser(r.Context()); user != nil {
		actor = user.ID
	}
	_ = h.audit.LogAudit(r.Context(), &domain.AuditEntry{
		UserID:     actor,
		Action:     "admin.set_org_tier",
		Resource:   "org",
		ResourceID: org.ID,
		Details:    fmt.Sprintf("from=%s to=%s reason=%q", previous, org.Tier, reason),
		IPAddress:  clientIP(r),
	})
}
