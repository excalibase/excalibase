package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/sites"
)

type siteEnvBody struct {
	Vars map[string]string `json:"vars"`
}

// presentDeploy marks the live deploy and leaves the site's variables out: they
// are read on their own route, which Developers alone reach.
func presentDeploy(deploy *sites.Deploy, live bool) deployResponse {
	shown := *deploy
	if shown.Server != nil {
		server := *shown.Server
		server.RuntimeVars = nil
		shown.Server = &server
	}
	return deployResponse{Deploy: &shown, Live: live}
}

func (h *SiteHandler) getEnv(w http.ResponseWriter, r *http.Request) {
	siteID, ok := sitePathID(w, r, "siteId")
	if !ok {
		return
	}
	vars, err := h.sites.RuntimeVars(r.Context(), chi.URLParam(r, "projectId"), siteID)
	if err != nil {
		writeSiteError(w, err)
		return
	}
	writeJSON(w, siteEnvBody{Vars: vars})
}

func (h *SiteHandler) putEnv(w http.ResponseWriter, r *http.Request) {
	siteID, ok := sitePathID(w, r, "siteId")
	if !ok {
		return
	}
	var body siteEnvBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, sites.MaxRuntimeVarsBodyBytes)).Decode(&body); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpError(w, "the variables are larger than a site may set", http.StatusRequestEntityTooLarge)
			return
		}
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	if body.Vars == nil {
		httpError(w, "send vars: the whole set of variables (an empty object clears them)", http.StatusBadRequest)
		return
	}
	if err := h.sites.SetRuntimeVars(r.Context(), chi.URLParam(r, "projectId"), siteID, body.Vars); err != nil {
		writeSiteError(w, err)
		return
	}
	writeJSON(w, siteEnvBody{Vars: body.Vars})
}
