package handler

import (
	"encoding/json"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/go-chi/chi/v5"
)

// Custom domains of a site (EXC-614): the user's own hostname, served through
// Cloudflare for SaaS once its DNS points at the zone and its certificate is issued.

type siteDomainRequest struct {
	Hostname string `json:"hostname"`
	// Validation is http (the default) or txt.
	Validation string `json:"validation"`
}

// SetDomainWriteLimiter bounds adds and removes: each one is Cloudflare calls on the platform's one token.
func (h *SiteHandler) SetDomainWriteLimiter(limiter func(http.Handler) http.Handler) {
	h.domainWrites = limiter
}

// SetDomainCheckLimiter bounds checks: since a check is what calls Cloudflare, it has its own budget.
func (h *SiteHandler) SetDomainCheckLimiter(limiter func(http.Handler) http.Handler) {
	h.domainChecks = limiter
}

func orPassThrough(limiter func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	if limiter == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return limiter
}

func (h *SiteHandler) domainRoutes(r chi.Router, developer func(http.Handler) http.Handler) {
	writes, checks := orPassThrough(h.domainWrites), orPassThrough(h.domainChecks)
	r.Get("/domains", h.available(h.listDomains))
	r.With(developer, writes).Post("/domains", h.available(h.addDomain))
	r.With(developer, checks).Post("/domains/{domainId}/refresh", h.available(h.refreshDomain))
	r.With(developer, writes).Delete("/domains/{domainId}", h.available(h.removeDomain))
}

func (h *SiteHandler) listDomains(w http.ResponseWriter, r *http.Request) {
	siteID, ok := sitePathID(w, r, "siteId")
	if !ok {
		return
	}
	listed, err := h.sites.ListHostnames(r.Context(), chi.URLParam(r, "projectId"), siteID)
	if err != nil {
		writeSiteError(w, err)
		return
	}
	writeJSON(w, listed)
}

func (h *SiteHandler) addDomain(w http.ResponseWriter, r *http.Request) {
	siteID, ok := sitePathID(w, r, "siteId")
	if !ok {
		return
	}
	var body siteDomainRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSiteBodyBytes)).Decode(&body); err != nil {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	added, err := h.sites.AddHostname(r.Context(), sites.AddHostnameRequest{
		ProjectID: chi.URLParam(r, "projectId"), SiteID: siteID, Hostname: body.Hostname,
		Validation: body.Validation, Actor: actorID(r),
	})
	if err != nil {
		writeSiteError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, added)
}

func (h *SiteHandler) refreshDomain(w http.ResponseWriter, r *http.Request) {
	siteID, domainID, ok := domainPathIDs(w, r)
	if !ok {
		return
	}
	refreshed, err := h.sites.RefreshHostname(r.Context(), chi.URLParam(r, "projectId"), siteID, domainID)
	if err != nil {
		writeSiteError(w, err)
		return
	}
	writeJSON(w, refreshed)
}

func (h *SiteHandler) removeDomain(w http.ResponseWriter, r *http.Request) {
	siteID, domainID, ok := domainPathIDs(w, r)
	if !ok {
		return
	}
	if err := h.sites.RemoveHostname(r.Context(), chi.URLParam(r, "projectId"), siteID, domainID); err != nil {
		writeSiteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func domainPathIDs(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	siteID, ok := sitePathID(w, r, "siteId")
	if !ok {
		return "", "", false
	}
	domainID, ok := sitePathID(w, r, "domainId")
	return siteID, domainID, ok
}
