package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

type AuditHandler struct{ svc *service.AuditService }

func NewAuditHandler(svc *service.AuditService) *AuditHandler { return &AuditHandler{svc: svc} }

func (h *AuditHandler) Routes(r chi.Router) {
	r.Post("/enable", h.Enable)
	r.Get("/logs", h.GetLogs)
	r.Get("/config", h.GetConfig)
}

func (h *AuditHandler) Enable(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	var cfg domain.AuditConfig
	json.NewDecoder(r.Body).Decode(&cfg)
	if err := h.svc.EnableAudit(r.Context(), projectID, cfg); err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "enabled"})
}

func (h *AuditHandler) GetLogs(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	lines := 100
	if l := r.URL.Query().Get("lines"); l != "" {
		if v, err := strconv.Atoi(l); err == nil { lines = v }
	}
	logs, err := h.svc.GetAuditLogs(r.Context(), projectID, lines)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"logs": logs})
}

func (h *AuditHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	cfg, err := h.svc.GetAuditConfig(r.Context(), projectID)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, cfg)
}
