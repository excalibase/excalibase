package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

const maxDomainBodyBytes = 1024

type AppDomains interface {
	Add(ctx context.Context, projectID, appID, hostname string) (*service.DomainView, error)
	List(projectID, appID string) ([]*service.DomainView, error)
	Verify(ctx context.Context, projectID, appID, id string) (*service.DomainView, error)
	Remove(ctx context.Context, projectID, appID, id string) error
	HostCertificate(ctx context.Context, projectID, appID string) (*service.HostCertificateView, error)
}

// AppDomainHandler lets a project point its own domains at its apps.
type AppDomainHandler struct {
	domains AppDomains
}

func NewAppDomainHandler(domains AppDomains) *AppDomainHandler {
	return &AppDomainHandler{domains: domains}
}

func (h *AppDomainHandler) Routes(r chi.Router) {
	r.Get("/", h.List)
	r.Post("/", h.Add)
	r.Post("/{domainId}/verify", h.Verify)
	r.Delete("/{domainId}", h.Remove)
}

func (h *AppDomainHandler) List(w http.ResponseWriter, r *http.Request) {
	projectID, appID, ok := domainAppPath(w, r)
	if !ok {
		return
	}
	views, err := h.domains.List(projectID, appID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, views)
}

func (h *AppDomainHandler) Add(w http.ResponseWriter, r *http.Request) {
	projectID, appID, ok := domainAppPath(w, r)
	if !ok {
		return
	}
	var body struct {
		Hostname string `json:"hostname"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxDomainBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	view, err := h.domains.Add(r.Context(), projectID, appID, body.Hostname)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, view)
}

func (h *AppDomainHandler) Verify(w http.ResponseWriter, r *http.Request) {
	projectID, appID, id, ok := domainPath(w, r)
	if !ok {
		return
	}
	view, err := h.domains.Verify(r.Context(), projectID, appID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, view)
}

func (h *AppDomainHandler) Remove(w http.ResponseWriter, r *http.Request) {
	projectID, appID, id, ok := domainPath(w, r)
	if !ok {
		return
	}
	if err := h.domains.Remove(r.Context(), projectID, appID, id); err != nil {
		writeDomainError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HostCertificate reports the certificate of the app's own hostname.
func (h *AppDomainHandler) HostCertificate(w http.ResponseWriter, r *http.Request) {
	projectID, appID, ok := domainAppPath(w, r)
	if !ok {
		return
	}
	view, err := h.domains.HostCertificate(r.Context(), projectID, appID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, view)
}

func domainAppPath(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return "", "", false
	}
	appID := chi.URLParam(r, "appId")
	if apphost.ValidateID(appID) != nil {
		httpError(w, "invalid appId", http.StatusBadRequest)
		return "", "", false
	}
	return projectID, appID, true
}

func domainPath(w http.ResponseWriter, r *http.Request) (string, string, string, bool) {
	projectID, appID, ok := domainAppPath(w, r)
	if !ok {
		return "", "", "", false
	}
	id := chi.URLParam(r, "domainId")
	if apphost.ValidateID(id) != nil {
		httpError(w, "invalid domainId", http.StatusBadRequest)
		return "", "", "", false
	}
	return projectID, appID, id, true
}

func writeDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, apphost.ErrInvalidDomain):
		httpError(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, apphost.ErrDomainNotFound), errors.Is(err, apphost.ErrAppNotFound):
		httpError(w, errNotFound, http.StatusNotFound)
	case errors.Is(err, apphost.ErrDomainLimit), errors.Is(err, apphost.ErrDomainExists), errors.Is(err, storage.ErrProjectBusy),
		errors.Is(err, apphost.ErrDomainClaimed), errors.Is(err, service.ErrDomainNotPointed),
		errors.Is(err, service.ErrDomainOnInternalService):
		httpError(w, err.Error(), http.StatusConflict)
	default:
		httpError(w, safeError(err), http.StatusInternalServerError)
	}
}
