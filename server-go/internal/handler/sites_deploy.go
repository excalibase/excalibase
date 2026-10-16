package handler

import (
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/go-chi/chi/v5"
)

// deploy takes the build output as the raw body (tar or tar.gz), as
// `curl --data-binary @dist.tgz` sends it; ?label= names the commit or ref.
func (h *SiteHandler) deploy(w http.ResponseWriter, r *http.Request) {
	siteID, ok := sitePathID(w, r, "siteId")
	if !ok {
		return
	}
	projectID := chi.URLParam(r, "projectId")
	ceiling, err := h.sites.MaxUploadBytes(r.Context(), projectID)
	if err != nil {
		writeSiteError(w, err)
		return
	}
	if r.ContentLength > ceiling {
		httpError(w, "the upload is larger than this project's deploys may be", http.StatusRequestEntityTooLarge)
		return
	}
	deploy, err := h.sites.Deploy(r.Context(), sites.DeployRequest{
		ProjectID: projectID, SiteID: siteID, Actor: actorID(r),
		Label: r.URL.Query().Get("label"), Body: http.MaxBytesReader(w, r.Body, ceiling),
	})
	if err != nil {
		writeSiteError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, deployResponse{Deploy: deploy, Live: true})
}

func (h *SiteHandler) listDeploys(w http.ResponseWriter, r *http.Request) {
	siteID, ok := sitePathID(w, r, "siteId")
	if !ok {
		return
	}
	projectID := chi.URLParam(r, "projectId")
	site, err := h.sites.Get(r.Context(), projectID, siteID)
	if err != nil {
		writeSiteError(w, err)
		return
	}
	deploys, err := h.sites.ListDeploys(r.Context(), projectID, siteID)
	if err != nil {
		writeSiteError(w, err)
		return
	}
	out := make([]deployResponse, 0, len(deploys))
	for _, deploy := range deploys {
		out = append(out, deployResponse{Deploy: deploy, Live: deploy.ID == site.LiveDeployID})
	}
	writeJSON(w, out)
}

func (h *SiteHandler) getDeploy(w http.ResponseWriter, r *http.Request) {
	siteID, ok := sitePathID(w, r, "siteId")
	if !ok {
		return
	}
	deployID, ok := sitePathID(w, r, "deployId")
	if !ok {
		return
	}
	projectID := chi.URLParam(r, "projectId")
	site, err := h.sites.Get(r.Context(), projectID, siteID)
	if err != nil {
		writeSiteError(w, err)
		return
	}
	deploy, err := h.sites.GetDeploy(r.Context(), projectID, siteID, deployID)
	if err != nil {
		writeSiteError(w, err)
		return
	}
	writeJSON(w, deployResponse{Deploy: deploy, Live: deploy.ID == site.LiveDeployID})
}

func (h *SiteHandler) rollback(w http.ResponseWriter, r *http.Request) {
	siteID, ok := sitePathID(w, r, "siteId")
	if !ok {
		return
	}
	deployID, ok := sitePathID(w, r, "deployId")
	if !ok {
		return
	}
	site, err := h.sites.Rollback(r.Context(), chi.URLParam(r, "projectId"), siteID, deployID)
	if err != nil {
		writeSiteError(w, err)
		return
	}
	writeJSON(w, presentSite(site))
}
