package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

type AuditHandler struct{ svc *service.AuditService }

func NewAuditHandler(svc *service.AuditService) *AuditHandler { return &AuditHandler{svc: svc} }

// Routes: reading is for developers; turning audit on restarts the database,
// so it is an admin's call, like tuning its settings.
func (h *AuditHandler) Routes(r chi.Router, admin func(http.Handler) http.Handler) {
	r.With(admin).Post("/enable", h.Enable)
	r.Get("/logs", h.GetLogs)
	r.Get("/config", h.GetConfig)
}

// auditBodyLimit bounds the enable request.
const auditBodyLimit = 4 << 10

// Enable answers 202: the database restarts to load pgaudit, which takes longer
// than a request may. Poll GET config until it reports the settings.
func (h *AuditHandler) Enable(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	var cfg domain.AuditConfig
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, auditBodyLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		httpError(w, "the body must be JSON like {\"enabled\":true,\"settings\":{\"pgaudit.log\":\"ddl,write\"}}", http.StatusBadRequest)
		return
	}
	if err := h.svc.EnableAudit(r.Context(), projectID, cfg); err != nil {
		httpError(w, safeError(err), auditStatus(err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "enabling",
		"note":   "the database restarts to load pgaudit; poll GET audit/config until its status is enabled",
	})
}

func auditStatus(err error) int {
	switch {
	case errors.Is(err, service.ErrAuditClasses), errors.Is(err, service.ErrAuditDisableUnsupported):
		return http.StatusBadRequest
	case errors.Is(err, service.ErrKubernetesOnly):
		return http.StatusConflict
	}
	return unsettledOr(err, http.StatusInternalServerError)
}

func (h *AuditHandler) GetLogs(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	lines := 100
	if l := r.URL.Query().Get("lines"); l != "" {
		if v, err := strconv.Atoi(l); err == nil {
			lines = v
		}
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
