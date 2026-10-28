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

// A database's read-only address (EXC-666): reads are Developer+, opening or
// closing its public port is Admin+.

const auditDBReadAddressUpdate = "db_endpoint.read_address.update"

// DBReadAddressAPI is the read-address half of the endpoint services.
type DBReadAddressAPI interface {
	ReadAddress(ctx context.Context, projectID string) (service.DBReadAddressView, error)
	SetReadAddress(ctx context.Context, projectID string, enabled bool) (service.DBReadAddressView, error)
}

type DBReadAddressHandler struct {
	api   DBReadAddressAPI
	audit auditWriter
}

func NewDBReadAddressHandler(api DBReadAddressAPI, audit auditWriter) *DBReadAddressHandler {
	return &DBReadAddressHandler{api: api, audit: audit}
}

// SetAPI wires the endpoint service, which is built after the routes.
func (h *DBReadAddressHandler) SetAPI(api DBReadAddressAPI) {
	h.api = api
}

func (h *DBReadAddressHandler) Routes(r chi.Router) {
	r.Get("/", h.get)
	r.Put("/", h.put)
}

type dbReadAddressInternal struct {
	Host             string `json:"host"`
	Port             int    `json:"port"`
	ConnectionString string `json:"connectionString"`
}

// dbReadAddressResponse: replicas are the copies beside the primary; reason
// (one-copy, single-host) says why there is no read address; publicOpen is
// whether the primary public port is open, which the read port needs.
type dbReadAddressResponse struct {
	ProjectID        string                 `json:"projectId"`
	Engine           string                 `json:"engine,omitempty"`
	Replicas         int                    `json:"replicas"`
	Reason           string                 `json:"reason,omitempty"`
	SingleHost       bool                   `json:"singleHost,omitempty"`
	Internal         *dbReadAddressInternal `json:"internal,omitempty"`
	PublicOffered    bool                   `json:"publicOffered"`
	PublicOpen       bool                   `json:"publicOpen"`
	Enabled          bool                   `json:"enabled"`
	Available        bool                   `json:"available"`
	Host             string                 `json:"host,omitempty"`
	Port             int                    `json:"port,omitempty"`
	ConnectionString string                 `json:"connectionString,omitempty"`
	CanChange        bool                   `json:"canChange"`
}

type dbReadAddressRequest struct {
	Enabled *bool `json:"enabled"`
}

func (h *DBReadAddressHandler) get(w http.ResponseWriter, r *http.Request) {
	projectID, ok := h.project(w, r)
	if !ok {
		return
	}
	view, err := h.api.ReadAddress(r.Context(), projectID)
	if err != nil {
		writeDBReadAddressError(w, projectID, err)
		return
	}
	writeJSON(w, readAddressResponseFor(r, view))
}

func (h *DBReadAddressHandler) put(w http.ResponseWriter, r *http.Request) {
	projectID, ok := h.project(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var body dbReadAddressRequest
	if err := decoder.Decode(&body); err != nil || body.Enabled == nil {
		httpError(w, errInvalidRequestBody, http.StatusBadRequest)
		return
	}
	view, err := h.api.SetReadAddress(r.Context(), projectID, *body.Enabled)
	h.record(r, projectID, *body.Enabled, err)
	if err != nil {
		writeDBReadAddressError(w, projectID, err)
		return
	}
	writeJSON(w, readAddressResponseFor(r, view))
}

func (h *DBReadAddressHandler) project(w http.ResponseWriter, r *http.Request) (string, bool) {
	projectID := chi.URLParam(r, "projectId")
	if !isValidID(projectID) {
		httpError(w, "invalid projectId", http.StatusBadRequest)
		return "", false
	}
	if h.api == nil {
		httpError(w, errDBEndpointNotConfigured, http.StatusServiceUnavailable)
		return "", false
	}
	return projectID, true
}

func readAddressResponseFor(r *http.Request, view service.DBReadAddressView) dbReadAddressResponse {
	out := dbReadAddressResponse{
		ProjectID: view.ProjectID, Engine: view.Engine, Replicas: view.Replicas, Reason: view.Reason,
		SingleHost: view.SingleHost, PublicOffered: view.PublicOffered, PublicOpen: view.PublicOpen,
		Enabled: view.Enabled, Available: view.Available, Host: view.Host, Port: view.Port,
		ConnectionString: view.ConnectionString,
		CanChange:        callerMayChangeDBEndpoint(r) && !view.SingleHost,
	}
	if view.Internal.Host != "" {
		out.Internal = &dbReadAddressInternal{Host: view.Internal.Host, Port: view.Internal.Port,
			ConnectionString: view.Internal.ConnectionString}
	}
	return out
}

// record audits every attempt with its outcome.
func (h *DBReadAddressHandler) record(r *http.Request, projectID string, enabled bool, failure error) {
	if h.audit == nil {
		return
	}
	outcome := "saved"
	if failure != nil {
		outcome = "refused"
	}
	encoded, _ := json.Marshal(map[string]any{"enabled": enabled, "outcome": outcome})
	now := time.Now()
	entry := &domain.AuditEntry{UserID: actorID(r), Action: auditDBReadAddressUpdate, Resource: "db_endpoint",
		ResourceID: projectID, ProjectID: projectID, Details: string(encoded), IPAddress: clientIP(r), Timestamp: &now}
	if err := h.audit.LogAudit(r.Context(), entry); err != nil {
		log.Printf("ERROR: audit %s on %s: %v", auditDBReadAddressUpdate, safeLog(projectID), err)
	}
}

func writeDBReadAddressError(w http.ResponseWriter, projectID string, err error) {
	switch {
	case errors.Is(err, service.ErrReadAddressNoReplica),
		errors.Is(err, service.ErrReadAddressNeedsPublicPort),
		errors.Is(err, service.ErrProjectNotActive),
		errors.Is(err, service.ErrDBEndpointUnsupported),
		errors.Is(err, storage.ErrProjectBusy):
		httpError(w, safeError(err), http.StatusConflict)
	case errors.Is(err, service.ErrDBEndpointNotConfigured),
		errors.Is(err, service.ErrReadAddressCopiesUnreadable),
		errors.Is(err, storage.ErrDBEndpointPortsExhausted):
		log.Printf("WARN: database read address of %s: %v", safeLog(projectID), err)
		httpError(w, refusalHead(err), http.StatusServiceUnavailable)
	case errors.Is(err, service.ErrDBEndpointNotObserved):
		httpError(w, safeError(err), http.StatusBadGateway)
	default:
		log.Printf("ERROR: database read address of %s: %v", safeLog(projectID), err)
		httpError(w, "the read address could not be read or changed", http.StatusInternalServerError)
	}
}

// refusalHead is the sentinel's own message, without the wrapped cause.
func refusalHead(err error) string {
	for _, sentinel := range []error{service.ErrDBEndpointNotConfigured, service.ErrReadAddressCopiesUnreadable, storage.ErrDBEndpointPortsExhausted} {
		if errors.Is(err, sentinel) {
			return sentinel.Error()
		}
	}
	return safeError(err)
}
