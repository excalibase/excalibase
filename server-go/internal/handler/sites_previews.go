// Package handler — previews of sites (EXC-660): which branches and pull
// requests of a connected repository get a preview, the Admin's opt-ins, and
// the previews themselves. Every route answers 404 unless previews are on.
package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
	"github.com/excalibase/provisioning-poc/internal/sites"
)

// SetAuditLog records the preview opt-ins an Admin changes; nil records nothing.
func (h *SiteGitHandler) SetAuditLog(audit auditWriter) { h.audit = audit }

// PreviewRoutes mounts the preview routes: reads for any member, the mode and removals for developers,
// and the opt-ins that reach past a preview (forks, secrets, API access, public links) for Admins.
func (h *SiteGitHandler) PreviewRoutes(r chi.Router, developer, admin func(http.Handler) http.Handler) {
	r.Get("/{siteId}/previews", h.previewsOn(h.listPreviews))
	r.With(developer).Put("/{siteId}/previews/mode", h.previewsOn(h.setPreviewMode))
	r.With(admin).Put("/{siteId}/previews/access", h.previewsOn(h.setPreviewAccess))
	r.With(developer).Delete("/{siteId}/previews/{previewId}", h.previewsOn(h.removePreview))
}

// previewsOn answers 404 while previews are not wired, and 503 with the reason while git builds
// are not offered here (off, or single-host); the feature flag itself is checked where the routes mount.
func (h *SiteGitHandler) previewsOn(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := h.builds.PreviewsAvailable()
		if errors.Is(err, sitebuilds.ErrPreviewsOff) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			writeSiteGitError(w, err)
			return
		}
		next(w, r)
	}
}

func (h *SiteGitHandler) listPreviews(w http.ResponseWriter, r *http.Request) {
	projectID, siteID, ok := projectSite(w, r)
	if !ok {
		return
	}
	overview, err := h.builds.Previews(r.Context(), projectID, siteID)
	if err != nil {
		writePreviewError(w, err)
		return
	}
	writeJSON(w, overview)
}

func (h *SiteGitHandler) setPreviewMode(w http.ResponseWriter, r *http.Request) {
	projectID, siteID, ok := projectSite(w, r)
	if !ok {
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSiteBodyBytes)).Decode(&body); err != nil {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	overview, err := h.builds.SetPreviewMode(r.Context(), projectID, siteID, body.Mode)
	if err != nil {
		writePreviewError(w, err)
		return
	}
	writeJSON(w, overview)
}

func (h *SiteGitHandler) setPreviewAccess(w http.ResponseWriter, r *http.Request) {
	projectID, siteID, ok := projectSite(w, r)
	if !ok {
		return
	}
	var access sitebuilds.PreviewAccess
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSiteBodyBytes)).Decode(&access); err != nil {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	overview, err := h.builds.SetPreviewAccess(r.Context(), projectID, siteID, access)
	if err != nil {
		writePreviewError(w, err)
		return
	}
	h.auditPreviewAccess(r, siteID, access)
	writeJSON(w, overview)
}

func (h *SiteGitHandler) removePreview(w http.ResponseWriter, r *http.Request) {
	projectID, siteID, ok := projectSite(w, r)
	if !ok {
		return
	}
	previewID, ok := sitePathID(w, r, "previewId")
	if !ok {
		return
	}
	if err := h.builds.RemovePreview(r.Context(), projectID, siteID, previewID); err != nil {
		writePreviewError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// auditPreviewAccess records the opt-ins as set: each one widens what a preview reaches.
func (h *SiteGitHandler) auditPreviewAccess(r *http.Request, siteID string, access sitebuilds.PreviewAccess) {
	if h.audit == nil {
		return
	}
	details, _ := json.Marshal(access)
	now := time.Now()
	entry := &domain.AuditEntry{
		Action: "site_preview.access.update", Resource: "site", ResourceID: siteID, ProjectID: chi.URLParam(r, "projectId"),
		Details: string(details), IPAddress: clientIP(r), Timestamp: &now, UserID: actorID(r),
	}
	if err := h.audit.LogAudit(r.Context(), entry); err != nil {
		log.Printf("ERROR: audit site_preview.access.update on %s: %v", siteID, err)
	}
}

func writePreviewError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sites.ErrPreviewNotFound):
		httpError(w, errNotFound, http.StatusNotFound)
	case errors.Is(err, sites.ErrPreviewRemoving):
		httpError(w, err.Error(), http.StatusConflict)
	default:
		writeSiteGitError(w, err)
	}
}
