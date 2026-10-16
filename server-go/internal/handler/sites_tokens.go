package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"

	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/go-chi/chi/v5"
)

// Deploy tokens (EXC-620): a CI credential for one site, made and revoked by a
// Developer or above. The raw token is in the create answer only.

type deployTokenRequest struct {
	Name string `json:"name"`
}

type issuedDeployTokenResponse struct {
	*sites.DeployToken
	Token string `json:"token"`
}

func (h *SiteHandler) listTokens(w http.ResponseWriter, r *http.Request) {
	siteID, ok := sitePathID(w, r, "siteId")
	if !ok {
		return
	}
	tokens, err := h.sites.ListDeployTokens(r.Context(), chi.URLParam(r, "projectId"), siteID)
	if err != nil {
		writeSiteError(w, err)
		return
	}
	writeJSON(w, tokens)
}

func (h *SiteHandler) createToken(w http.ResponseWriter, r *http.Request) {
	siteID, ok := sitePathID(w, r, "siteId")
	if !ok {
		return
	}
	var body deployTokenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSiteBodyBytes)).Decode(&body); err != nil {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	raw, token, err := h.sites.CreateDeployToken(r.Context(), chi.URLParam(r, "projectId"), siteID, body.Name, actorID(r))
	if err != nil {
		writeSiteError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.auditToken(r, "site_deploy_token.create", token.ID, siteID, token.Prefix)
	writeJSONStatus(w, http.StatusCreated, issuedDeployTokenResponse{DeployToken: token, Token: raw})
}

func (h *SiteHandler) revokeToken(w http.ResponseWriter, r *http.Request) {
	siteID, ok := sitePathID(w, r, "siteId")
	if !ok {
		return
	}
	tokenID, ok := sitePathID(w, r, "tokenId")
	if !ok {
		return
	}
	if err := h.sites.RevokeDeployToken(r.Context(), chi.URLParam(r, "projectId"), siteID, tokenID); err != nil {
		writeSiteError(w, err)
		return
	}
	h.auditToken(r, "site_deploy_token.revoke", tokenID, siteID, "")
	w.WriteHeader(http.StatusNoContent)
}

// auditToken records a deploy token made or revoked, by its prefix only.
func (h *SiteHandler) auditToken(r *http.Request, action, tokenID, siteID, prefix string) {
	if h.audit == nil {
		return
	}
	details, _ := json.Marshal(map[string]string{"siteId": siteID, "tokenPrefix": prefix})
	now := time.Now()
	entry := &domain.AuditEntry{
		Action: action, Resource: "site_deploy_token", ResourceID: tokenID, ProjectID: chi.URLParam(r, "projectId"),
		Details: string(details), IPAddress: clientIP(r), Timestamp: &now, UserID: actorID(r),
	}
	if err := h.audit.LogAudit(r.Context(), entry); err != nil {
		log.Printf("ERROR: audit %s on %s: %v", action, tokenID, err)
	}
}
