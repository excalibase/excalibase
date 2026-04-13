package handler

import (
	"encoding/json"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func (h *ProvisioningHandler) ProvisionBYOC(w http.ResponseWriter, r *http.Request) {
	var req domain.BYOCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.ProjectName == "" || req.Host == "" || req.Port == 0 ||
		req.Database == "" || req.Username == "" || req.Password == "" {
		httpError(w, "projectName, host, port, database, username, and password are required", http.StatusBadRequest)
		return
	}

	resp, err := h.svc.ProvisionBYOC(r.Context(), req)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, resp)
}
