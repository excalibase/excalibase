package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

// The public database endpoint API (EXC-410). One GET to see what the
// project has and what each TLS choice would look like, one PUT to change
// either setting. Public reach is opt-in and off until this PUT says
// otherwise; turning it off here deletes the Service, so the port stops
// answering.

// errDBEndpointNotConfigured is returned when no endpoint service is wired —
// the API fails closed rather than pretending a setting was saved.
const errDBEndpointNotConfigured = "public database endpoints are not configured"

// DBEndpointAPI is the slice of the endpoint service this handler uses.
type DBEndpointAPI interface {
	Describe(ctx context.Context, projectID string) (service.DBEndpointView, error)
	SetPublic(ctx context.Context, projectID string, public bool) (service.DBEndpointView, error)
	SetRequireTLS(ctx context.Context, projectID string, requireTLS bool) (service.DBEndpointView, error)
}

// SetDBEndpointService wires the public database endpoint (EXC-410).
func (h *ProvisioningHandler) SetDBEndpointService(api DBEndpointAPI) {
	h.dbEndpoints = api
}

// dbEndpointConnectionStrings is both connection strings for the endpoint.
// Both are always present: a customer deciding whether to require TLS should
// see exactly what each choice costs them, not be told about the one they
// already have.
type dbEndpointConnectionStrings struct {
	RequireTLS     string `json:"requireTls"`
	AllowPlaintext string `json:"allowPlaintext"`
}

// dbEndpointInternal is the in-cluster endpoint, which exists for every
// project and needs no public port at all. An app hosted on this platform
// beside the database should connect here.
type dbEndpointInternal struct {
	Host             string `json:"host"`
	Port             int    `json:"port"`
	ConnectionString string `json:"connectionString"`
}

// dbEndpointResponse is the wire shape of GET/PUT
// /api/projects/{projectId}/db-endpoint.
//
//	publicEnabled  the customer's choice; false means no Service exists
//	available      observation: whether the port is answering right now.
//	               False while the project is paused, deleting or restoring
//	port           0 when the project holds none
//	caCertificate  the cluster CA, so sslmode=verify-full works. Present
//	               only while the endpoint is actually up
type dbEndpointResponse struct {
	ProjectID         string                      `json:"projectId"`
	PublicEnabled     bool                        `json:"publicEnabled"`
	Available         bool                        `json:"available"`
	Host              string                      `json:"host"`
	Port              int                         `json:"port"`
	RequireTLS        bool                        `json:"requireTls"`
	Database          string                      `json:"database"`
	Username          string                      `json:"username"`
	ConnectionStrings dbEndpointConnectionStrings `json:"connectionStrings"`
	CACertificate     string                      `json:"caCertificate"`
	Internal          dbEndpointInternal          `json:"internal"`
}

// dbEndpointRequest is the PUT body. Both fields are optional: a body that
// says nothing about a setting leaves it alone, so Studio can toggle one
// without having to restate the other.
type dbEndpointRequest struct {
	PublicEnabled *bool `json:"publicEnabled"`
	RequireTLS    *bool `json:"requireTls"`
}

// GetDBEndpoint serves GET /api/projects/{projectId}/db-endpoint.
func (h *ProvisioningHandler) GetDBEndpoint(w http.ResponseWriter, r *http.Request) {
	projectID, ok := h.dbEndpointRequestProject(w, r)
	if !ok {
		return
	}
	view, err := h.dbEndpoints.Describe(r.Context(), projectID)
	if err != nil {
		writeDBEndpointError(w, err)
		return
	}
	writeJSON(w, dbEndpointResponseFor(view))
}

// PutDBEndpoint serves PUT /api/projects/{projectId}/db-endpoint with body
// {"publicEnabled": true, "requireTls": true}.
//
// The TLS choice is applied first, so a caller that turns the endpoint on and
// TLS off in one request is handed a connection string that matches what they
// asked for rather than one for the setting they just replaced.
func (h *ProvisioningHandler) PutDBEndpoint(w http.ResponseWriter, r *http.Request) {
	projectID, ok := h.dbEndpointRequestProject(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	var body dbEndpointRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, errInvalidRequestBody, http.StatusBadRequest)
		return
	}
	view, err := h.applyDBEndpoint(r.Context(), projectID, body)
	if err != nil {
		writeDBEndpointError(w, err)
		return
	}
	writeJSON(w, dbEndpointResponseFor(view))
}

// applyDBEndpoint writes whichever settings the body named, and reads the
// endpoint back when it named none.
func (h *ProvisioningHandler) applyDBEndpoint(ctx context.Context, projectID string, body dbEndpointRequest) (service.DBEndpointView, error) {
	var (
		view    service.DBEndpointView
		applied bool
		err     error
	)
	if body.RequireTLS != nil {
		if view, err = h.dbEndpoints.SetRequireTLS(ctx, projectID, *body.RequireTLS); err != nil {
			return service.DBEndpointView{}, err
		}
		applied = true
	}
	if body.PublicEnabled != nil {
		if view, err = h.dbEndpoints.SetPublic(ctx, projectID, *body.PublicEnabled); err != nil {
			return service.DBEndpointView{}, err
		}
		applied = true
	}
	if !applied {
		return h.dbEndpoints.Describe(ctx, projectID)
	}
	return view, nil
}

// dbEndpointRequestProject validates the path project and the wiring shared
// by both verbs, writing the error response itself when either fails.
func (h *ProvisioningHandler) dbEndpointRequestProject(w http.ResponseWriter, r *http.Request) (string, bool) {
	projectID := chi.URLParam(r, "projectId")
	if !isValidID(projectID) {
		httpError(w, "invalid projectId", http.StatusBadRequest)
		return "", false
	}
	if h.dbEndpoints == nil {
		httpError(w, errDBEndpointNotConfigured, http.StatusServiceUnavailable)
		return "", false
	}
	return projectID, true
}

// writeDBEndpointError maps each refusal to what it means for the caller.
// They are deliberately distinct: "this platform offers none", "this project
// cannot have one", "there is no port left" and "we asked and could not
// confirm it" call for four different reactions.
func writeDBEndpointError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrDBEndpointNotConfigured),
		errors.Is(err, storage.ErrDBEndpointPortsExhausted):
		httpError(w, safeError(err), http.StatusServiceUnavailable)
	case errors.Is(err, service.ErrDBEndpointUnsupported):
		httpError(w, safeError(err), http.StatusConflict)
	case errors.Is(err, service.ErrDBEndpointNotObserved):
		httpError(w, safeError(err), http.StatusBadGateway)
	default:
		httpError(w, safeError(err), http.StatusInternalServerError)
	}
}

func dbEndpointResponseFor(view service.DBEndpointView) dbEndpointResponse {
	return dbEndpointResponse{
		ProjectID:     view.ProjectID,
		PublicEnabled: view.Enabled,
		Available:     view.Available,
		Host:          view.Host,
		Port:          view.Port,
		RequireTLS:    view.RequireTLS,
		Database:      view.Database,
		Username:      view.Username,
		ConnectionStrings: dbEndpointConnectionStrings{
			RequireTLS:     view.Connection.RequireTLS,
			AllowPlaintext: view.Connection.AllowPlaintext,
		},
		CACertificate: view.CACertificate,
		Internal: dbEndpointInternal{
			Host:             view.Internal.Host,
			Port:             view.Internal.Port,
			ConnectionString: view.Internal.ConnectionString,
		},
	}
}
