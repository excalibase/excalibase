// Package handler — Sites (EXC-353): a project's built frontend, deployed by
// uploading its build output and served from Cloudflare.
//
// Mounted at /api/projects/{projectId}/sites behind the project's access
// guard. An install without a Cloudflare account answers every route 503 once
// the caller is authorised, so Studio and CI learn why rather than meeting a 404.
package handler

import (
	"encoding/json"
	"errors"
	"log"
	"math"
	"net/http"
	"strconv"

	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/go-chi/chi/v5"
)

const maxSiteBodyBytes = 4 * 1024

// SiteHandler serves the Sites API; a nil or unavailable service means Sites is off here.
type SiteHandler struct {
	sites *sites.Service
	// audit records deploy tokens made and revoked (EXC-620); nil records nothing.
	audit auditWriter
	// standing says whether a listed token's creator may still deploy.
	standing tokenStanding
	// domainWrites rate-limits adding and removing custom domains (EXC-614); nil limits nothing.
	domainWrites func(http.Handler) http.Handler
	// domainChecks rate-limits checking custom domains; nil limits nothing.
	domainChecks func(http.Handler) http.Handler
}

// SetAuditLog records every deploy token made or revoked.
func (h *SiteHandler) SetAuditLog(audit auditWriter) { h.audit = audit }

func NewSiteHandler(service *sites.Service) *SiteHandler { return &SiteHandler{sites: service} }

// Routes mounts the site tree; developer gates every write.
func (h *SiteHandler) Routes(r chi.Router, developer func(http.Handler) http.Handler) {
	r.Get("/", h.available(h.list))
	r.With(developer).Post("/", h.available(h.create))
	r.Get("/limits", h.available(h.limits))
	r.Route("/{siteId}", func(r chi.Router) {
		r.Get("/", h.available(h.get))
		r.With(developer).Patch("/", h.available(h.update))
		r.With(developer).Delete("/", h.available(h.remove))
		r.With(developer).Post("/deploy", h.available(h.deploy))
		r.Get("/deploys", h.available(h.listDeploys))
		r.Get("/deploys/{deployId}", h.available(h.getDeploy))
		r.With(developer).Post("/deploys/{deployId}/rollback", h.available(h.rollback))
		r.With(developer).Get("/tokens", h.available(h.listTokens))
		r.With(developer).Post("/tokens", h.available(h.createToken))
		r.With(developer).Delete("/tokens/{tokenId}", h.available(h.revokeToken))
		h.domainRoutes(r, developer)
	})
}

func (h *SiteHandler) available(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.sites.Available() {
			httpError(w, sites.ErrSitesUnavailable.Error(), http.StatusServiceUnavailable)
			return
		}
		next(w, r)
	}
}

// siteResponse adds the URL the site is served at.
type siteResponse struct {
	*sites.Site
	URL string `json:"url"`
}

func presentSite(site *sites.Site) siteResponse {
	return siteResponse{Site: site, URL: sites.Origin(site)}
}

// deployResponse marks the deploy the site serves now.
type deployResponse struct {
	*sites.Deploy
	Live bool `json:"live"`
}

type siteCreateRequest struct {
	Name string `json:"name"`
	// Kind is static (the default) or server, offered with the sitesserver feature.
	Kind        string `json:"kind"`
	SPAFallback *bool  `json:"spaFallback"`
}

type siteUpdateRequest struct {
	SPAFallback *bool `json:"spaFallback"`
}

func (h *SiteHandler) list(w http.ResponseWriter, r *http.Request) {
	listed, err := h.sites.List(r.Context(), chi.URLParam(r, "projectId"))
	if err != nil {
		writeSiteError(w, err)
		return
	}
	out := make([]siteResponse, 0, len(listed))
	for _, site := range listed {
		out = append(out, presentSite(site))
	}
	writeJSON(w, out)
}

func (h *SiteHandler) create(w http.ResponseWriter, r *http.Request) {
	var body siteCreateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSiteBodyBytes)).Decode(&body); err != nil {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	kind := body.Kind
	if kind == "" {
		kind = sites.KindStatic
	}
	site, err := h.sites.CreateKind(r.Context(), chi.URLParam(r, "projectId"), body.Name, kind, body.SPAFallback, actorID(r))
	if err != nil {
		writeSiteError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, presentSite(site))
}

func (h *SiteHandler) get(w http.ResponseWriter, r *http.Request) {
	siteID, ok := sitePathID(w, r, "siteId")
	if !ok {
		return
	}
	site, err := h.sites.Get(r.Context(), chi.URLParam(r, "projectId"), siteID)
	if err != nil {
		writeSiteError(w, err)
		return
	}
	writeJSON(w, presentSite(site))
}

func (h *SiteHandler) update(w http.ResponseWriter, r *http.Request) {
	siteID, ok := sitePathID(w, r, "siteId")
	if !ok {
		return
	}
	var body siteUpdateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSiteBodyBytes)).Decode(&body); err != nil {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	if body.SPAFallback == nil {
		httpError(w, "nothing to change: send spaFallback", http.StatusBadRequest)
		return
	}
	site, err := h.sites.UpdateSettings(r.Context(), chi.URLParam(r, "projectId"), siteID, *body.SPAFallback)
	if err != nil {
		writeSiteError(w, err)
		return
	}
	writeJSON(w, presentSite(site))
}

func (h *SiteHandler) remove(w http.ResponseWriter, r *http.Request) {
	siteID, ok := sitePathID(w, r, "siteId")
	if !ok {
		return
	}
	if err := h.sites.Delete(r.Context(), chi.URLParam(r, "projectId"), siteID); err != nil {
		writeSiteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// limitsResponse is what the project's plan lets its sites hold (EXC-620).
type limitsResponse struct {
	MaxSites          int   `json:"maxSites"`
	MaxDeployBytes    int64 `json:"maxDeployBytes"`
	MaxFiles          int   `json:"maxFiles"`
	MaxFileBytes      int64 `json:"maxFileBytes"`
	MaxDeploysPerHour int   `json:"maxDeploysPerHour"`
	MaxCustomDomains  int   `json:"maxCustomDomains"`
	// Server-rendered sites (EXC-661): how many, and their code size uncompressed.
	MaxServerSites       int   `json:"maxServerSites"`
	MaxServerBundleBytes int64 `json:"maxServerBundleBytes"`
}

func (h *SiteHandler) limits(w http.ResponseWriter, r *http.Request) {
	limits, err := h.sites.Limits(r.Context(), chi.URLParam(r, "projectId"))
	if err != nil {
		writeSiteError(w, err)
		return
	}
	writeJSON(w, limitsResponse{MaxSites: limits.MaxSites, MaxDeployBytes: limits.MaxTotalBytes, MaxFiles: limits.MaxFiles,
		MaxFileBytes: limits.MaxFileBytes, MaxDeploysPerHour: limits.MaxDeploysPerHour, MaxCustomDomains: limits.MaxCustomDomains,
		MaxServerSites: limits.MaxServerSites, MaxServerBundleBytes: limits.MaxServerBundleBytes})
}

// sitePathID refuses an id that is not a plain identifier before any lookup.
func sitePathID(w http.ResponseWriter, r *http.Request, param string) (string, bool) {
	id := chi.URLParam(r, param)
	if !isValidID(id) {
		httpError(w, "invalid "+param, http.StatusBadRequest)
		return "", false
	}
	return id, true
}

// writeSiteError maps the service's refusals; anything else is logged and answered without detail.
func writeSiteError(w http.ResponseWriter, err error) {
	var archiveErr *sites.ArchiveError
	var inputErr *sites.InputError
	var publishErr *sites.PublishError
	var tooLarge *http.MaxBytesError
	var rateErr *sites.RateLimitError
	switch {
	case errors.Is(err, sites.ErrServerSitesOff):
		// As an unmounted route answers (EXC-554), so a dark feature is not advertised.
		http.Error(w, "404 page not found", http.StatusNotFound)
	case errors.Is(err, sites.ErrSitesUnavailable):
		httpError(w, sites.ErrSitesUnavailable.Error(), http.StatusServiceUnavailable)
	case errors.Is(err, sites.ErrCustomDomainsUnavailable):
		httpError(w, sites.ErrCustomDomainsUnavailable.Error()+"; the site's own hostname still serves it", http.StatusServiceUnavailable)
	case errors.Is(err, sites.ErrSiteNotFound), errors.Is(err, sites.ErrDeployNotFound), errors.Is(err, sites.ErrDeployTokenNotFound),
		errors.Is(err, sites.ErrHostnameNotFound):
		httpError(w, errNotFound, http.StatusNotFound)
	case errors.Is(err, sites.ErrSiteNameTaken), errors.Is(err, sites.ErrSiteLimitReached), errors.Is(err, sites.ErrSiteBusy), errors.Is(err, sites.ErrLeaseLost),
		errors.Is(err, sites.ErrDeployNotServable), errors.Is(err, sites.ErrRollbackNeedsFiles), errors.Is(err, sites.ErrDeployTokenLimitReached),
		errors.Is(err, sites.ErrHostnameTaken), errors.Is(err, sites.ErrHostnameLimitReached), errors.Is(err, sites.ErrCustomDomainsFull),
		errors.Is(err, sites.ErrProjectNotServing), errors.Is(err, sites.ErrServerSiteLimitReached), errors.Is(err, sites.ErrServerCodeGone):
		httpError(w, err.Error(), http.StatusConflict)
	case errors.As(err, &rateErr):
		w.Header().Set("Retry-After", strconv.FormatInt(int64(math.Ceil(rateErr.RetryAfter.Seconds())), 10))
		httpError(w, err.Error(), http.StatusTooManyRequests)
	case errors.Is(err, sites.ErrTooManyDeploys), errors.Is(err, sites.ErrProjectDeployRunning):
		w.Header().Set("Retry-After", "60")
		httpError(w, err.Error(), http.StatusTooManyRequests)
	case errors.As(err, &tooLarge):
		httpError(w, "the upload is larger than this project's deploys may be", http.StatusRequestEntityTooLarge)
	case errors.As(err, &archiveErr) && archiveErr.TooLarge:
		httpError(w, archiveErr.Error(), http.StatusRequestEntityTooLarge)
	case errors.As(err, &archiveErr), errors.As(err, &inputErr):
		httpError(w, err.Error(), http.StatusBadRequest)
	case errors.As(err, &publishErr):
		// The service logged the cause; the caller gets the fixed message for the step.
		httpError(w, publishErr.Error(), http.StatusBadGateway)
	default:
		log.Printf("sites: %v", err)
		httpError(w, "internal error", http.StatusInternalServerError)
	}
}
