package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// The database allow-list (EXC-665): who may reach the project's public port.
// Mounted under /db-endpoint, so reads are Developer+ and writes Admin+.

const auditDBAllowListUpdate = "db_endpoint.allowed_sources.update"

// DBAllowListAPI is the slice of the endpoint service this handler uses.
type DBAllowListAPI interface {
	AllowedSources(ctx context.Context, projectID string) (service.DBAllowListView, error)
	SetAllowedSources(ctx context.Context, projectID string, list domain.DBSourceAllowList) (service.DBAllowListView, error)
}

type DBAllowListHandler struct {
	api   DBAllowListAPI
	audit auditWriter
}

func NewDBAllowListHandler(api DBAllowListAPI, audit auditWriter) *DBAllowListHandler {
	return &DBAllowListHandler{api: api, audit: audit}
}

// SetAPI wires the endpoint service, which is built after the routes.
func (h *DBAllowListHandler) SetAPI(api DBAllowListAPI) {
	h.api = api
}

func (h *DBAllowListHandler) Routes(r chi.Router) {
	r.Get("/", h.get)
	r.Put("/", h.put)
}

// dbAllowListResponse: access is unset, any, listed or host (a single host's
// loopback). callerAddress is this request's client address, for "Add my address".
type dbAllowListResponse struct {
	ProjectID     string   `json:"projectId"`
	Access        string   `json:"access"`
	Sources       []string `json:"sources"`
	Max           int      `json:"max"`
	PublicEnabled bool     `json:"publicEnabled"`
	SingleHost    bool     `json:"singleHost,omitempty"`
	CanChange     bool     `json:"canChange"`
	CallerAddress string   `json:"callerAddress"`
}

type dbAllowListRequest struct {
	AllowAny bool     `json:"allowAny"`
	Sources  []string `json:"sources"`
}

func (h *DBAllowListHandler) get(w http.ResponseWriter, r *http.Request) {
	projectID, ok := allowListProject(w, r)
	if !ok || !h.configured(w) {
		return
	}
	view, err := h.api.AllowedSources(r.Context(), projectID)
	if err != nil {
		writeDBAllowListError(w, projectID, err)
		return
	}
	writeJSON(w, allowListResponseFor(r, view))
}

func (h *DBAllowListHandler) put(w http.ResponseWriter, r *http.Request) {
	projectID, ok := allowListProject(w, r)
	if !ok || !h.configured(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var body dbAllowListRequest
	if err := decoder.Decode(&body); err != nil {
		httpError(w, errInvalidRequestBody, http.StatusBadRequest)
		return
	}
	list, err := domain.ParseDBSourceAllowList(body.AllowAny, body.Sources)
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	previous, err := h.api.AllowedSources(r.Context(), projectID)
	if err != nil {
		writeDBAllowListError(w, projectID, err)
		return
	}
	view, err := h.api.SetAllowedSources(r.Context(), projectID, list)
	if err != nil {
		writeDBAllowListError(w, projectID, err)
		return
	}
	h.record(r, projectID, previous, view)
	writeJSON(w, allowListResponseFor(r, view))
}

func allowListProject(w http.ResponseWriter, r *http.Request) (string, bool) {
	projectID := chi.URLParam(r, "projectId")
	if !isValidID(projectID) {
		httpError(w, "invalid projectId", http.StatusBadRequest)
		return "", false
	}
	return projectID, true
}

// configured fails closed while no endpoint service is wired.
func (h *DBAllowListHandler) configured(w http.ResponseWriter) bool {
	if h.api == nil {
		httpError(w, errDBEndpointNotConfigured, http.StatusServiceUnavailable)
		return false
	}
	return true
}

func allowListResponseFor(r *http.Request, view service.DBAllowListView) dbAllowListResponse {
	access := string(view.Access)
	if view.Access == domain.DBSourceAccessUnset {
		access = "unset"
	}
	sources := view.Sources
	if sources == nil {
		sources = []string{}
	}
	return dbAllowListResponse{
		ProjectID: view.ProjectID, Access: access, Sources: sources, Max: view.Max,
		PublicEnabled: view.PublicEnabled, SingleHost: view.SingleHost,
		CanChange:     callerMayChangeDBEndpoint(r) && !view.SingleHost,
		CallerAddress: clientIP(r),
	}
}

func (h *DBAllowListHandler) record(r *http.Request, projectID string, previous, current service.DBAllowListView) {
	if h.audit == nil {
		return
	}
	details, _ := json.Marshal(map[string]any{
		"previousAccess": string(previous.Access), "previousSources": previous.Sources,
		"access": string(current.Access), "sources": current.Sources,
	})
	now := time.Now()
	entry := &domain.AuditEntry{UserID: actorID(r), Action: auditDBAllowListUpdate, Resource: "db_endpoint",
		ResourceID: projectID, ProjectID: projectID, Details: string(details), IPAddress: clientIP(r), Timestamp: &now}
	if err := h.audit.LogAudit(r.Context(), entry); err != nil {
		log.Printf("ERROR: audit %s on %s: %v", auditDBAllowListUpdate, safeLog(projectID), err)
	}
}

func writeDBAllowListError(w http.ResponseWriter, projectID string, err error) {
	switch {
	case errors.Is(err, service.ErrDBAllowListOff):
		httpError(w, "not found", http.StatusNotFound)
	case errors.Is(err, domain.ErrInvalidDBSourceAllowList):
		httpError(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, service.ErrDBEndpointUnsupported),
		errors.Is(err, storage.ErrProjectBusy):
		httpError(w, safeError(err), http.StatusConflict)
	case errors.Is(err, service.ErrDBEndpointNotObserved),
		errors.Is(err, service.ErrDBAllowListNotObserved):
		httpError(w, safeError(err), http.StatusBadGateway)
	default:
		log.Printf("ERROR: database allow-list of %s: %v", safeLog(projectID), err)
		httpError(w, "the allow-list could not be read or saved", http.StatusInternalServerError)
	}
}
