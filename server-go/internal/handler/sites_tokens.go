package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/go-chi/chi/v5"
)

// Deploy tokens (EXC-620): a CI credential for one site, made and revoked by a
// Developer or above. The raw token is in the create answer only.

type deployTokenRequest struct {
	Name string `json:"name"`
	// ExpiresInDays is the token's lifetime; absent is 90 days, 365 at most.
	ExpiresInDays *int `json:"expiresInDays,omitempty"`
}

// deployTokenFor turns the body into a request, capping the expiry at the
// caller's own token unless it is a Studio session, which is renewed by login.
func deployTokenFor(r *http.Request, siteID string, body deployTokenRequest) (sites.DeployTokenRequest, error) {
	request := sites.DeployTokenRequest{ProjectID: chi.URLParam(r, "projectId"), SiteID: siteID, Name: body.Name, Actor: actorID(r)}
	if body.ExpiresInDays != nil {
		days := *body.ExpiresInDays
		if days < 1 || days > int(sites.MaxDeployTokenLifetime/(24*time.Hour)) {
			return request, &sites.InputError{Reason: "expiresInDays must be between 1 and 365"}
		}
		request.Lifetime = time.Duration(days) * 24 * time.Hour
	}
	if caller := auth.GetToken(r.Context()); caller != nil && !auth.IsSessionToken(caller) {
		request.NotAfter = caller.ExpiresAt
	}
	return request, nil
}

// tokenStanding answers whether a token's creator may still deploy; the
// middleware's DeployTokenStanding runs the deploy route's own checks.
type tokenStanding interface {
	CreatorCanDeploy(ctx context.Context, token *sites.DeployToken) (bool, error)
}

// SetTokenStanding wires the check the token list reports.
func (h *SiteHandler) SetTokenStanding(standing tokenStanding) { h.standing = standing }

type listedDeployTokenResponse struct {
	sites.DeployToken
	// CreatorCanDeploy is false once the creator lost the developer role or was deactivated.
	CreatorCanDeploy bool `json:"creatorCanDeploy"`
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
	listed, err := h.withStanding(r.Context(), tokens)
	if err != nil {
		log.Printf("site deploy tokens: %v", err)
		httpError(w, "could not check who may still deploy with these tokens", http.StatusInternalServerError)
		return
	}
	writeJSON(w, listed)
}

func (h *SiteHandler) withStanding(ctx context.Context, tokens []*sites.DeployToken) ([]listedDeployTokenResponse, error) {
	if h.standing == nil {
		return nil, errors.New("no token standing check is wired")
	}
	listed := make([]listedDeployTokenResponse, 0, len(tokens))
	for _, token := range tokens {
		canDeploy, err := h.standing.CreatorCanDeploy(ctx, token)
		if err != nil {
			return nil, err
		}
		listed = append(listed, listedDeployTokenResponse{DeployToken: *token, CreatorCanDeploy: canDeploy})
	}
	return listed, nil
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
	request, err := deployTokenFor(r, siteID, body)
	if err != nil {
		writeSiteError(w, err)
		return
	}
	raw, token, err := h.sites.CreateDeployToken(r.Context(), request)
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
