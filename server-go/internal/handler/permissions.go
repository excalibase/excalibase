// Package handler — API permissions (EXC-370 step C, docs/features/permissions.md).
//
// Mounted under /api/provision/{projectId}/ as permissions/, tracked-functions/
// and function-permissions/. The project sub-router already checks auth,
// project access and the Developer role; the engine reads GET permissions/
// with its policies:read capability and may write nothing here.
//
// Every successful write publishes "policies.{projectId}.changed" so every
// engine replica drops the project's cached permissions and schemas.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/permissions"
	"github.com/excalibase/provisioning-poc/internal/projectdb"
	"github.com/excalibase/provisioning-poc/internal/schema"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

// maxPermissionBody bounds one permission object; 200 expression nodes fit
// many times over.
const maxPermissionBody = 64 << 10

const (
	routeTablePermission    = "/tables/{table}/roles/{role}/{operation}"
	routeTrackedFunction    = "/{function}"
	routeFunctionPermission = "/{function}/roles/{role}"

	errPermissionsUnavailable = "permissions could not be read"
	errPermissionsNotSaved    = "the change could not be saved"
)

// FunctionInspector reads a function's live definition from the project's
// own database.
type FunctionInspector interface {
	FunctionDetails(ctx context.Context, projectID, schemaName, name string) ([]schema.FunctionDetail, error)
}

// PermissionHandler serves a project's API permissions.
type PermissionHandler struct {
	store     storage.PermissionStore
	projects  ProjectFinder
	inspector FunctionInspector
	publisher PolicyChangePublisher // optional
}

func NewPermissionHandler(store storage.PermissionStore, projects ProjectFinder, inspector FunctionInspector) *PermissionHandler {
	return &PermissionHandler{store: store, projects: projects, inspector: inspector}
}

// SetPublisher wires the NATS publisher after construction.
func (h *PermissionHandler) SetPublisher(p PolicyChangePublisher) { h.publisher = p }

// PermissionRoutes mounts under /permissions.
func (h *PermissionHandler) PermissionRoutes(r chi.Router) {
	r.Get("/", h.Document)
	r.Put(routeTablePermission, h.PutPermission)
	r.Delete(routeTablePermission, h.DeletePermission)
}

// TrackedFunctionRoutes mounts under /tracked-functions.
func (h *PermissionHandler) TrackedFunctionRoutes(r chi.Router) {
	r.Post("/", h.TrackFunction)
	r.Delete(routeTrackedFunction, h.UntrackFunction)
}

// FunctionPermissionRoutes mounts under /function-permissions.
func (h *PermissionHandler) FunctionPermissionRoutes(r chi.Router) {
	r.Put(routeFunctionPermission, h.PutFunctionPermission)
	r.Delete(routeFunctionPermission, h.DeleteFunctionPermission)
}

// Document is the engine's read: the project's whole permission set.
func (h *PermissionHandler) Document(w http.ResponseWriter, r *http.Request) {
	projectID, ok := knownProjectFromPath(w, r, h.projects)
	if !ok {
		return
	}
	doc, err := h.store.Document(r.Context(), projectID)
	if err != nil {
		log.Printf("permissions: read document of %s: %v", projectID, err)
		httpError(w, errPermissionsUnavailable, http.StatusInternalServerError)
		return
	}
	writeJSON(w, doc)
}

// tablePermissionFromPath validates the table, role and operation segments.
func tablePermissionFromPath(w http.ResponseWriter, r *http.Request) (domain.TablePermission, bool) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return domain.TablePermission{}, false
	}
	perm := domain.TablePermission{
		ProjectID: projectID, Table: chi.URLParam(r, "table"),
		Role: chi.URLParam(r, "role"), Operation: chi.URLParam(r, "operation"),
	}
	for _, err := range []error{
		permissions.ValidateQualifiedName(perm.Table), permissions.ValidateRole(perm.Role),
		permissions.ValidateOperation(perm.Operation),
	} {
		if err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return domain.TablePermission{}, false
		}
	}
	return perm, true
}

func (h *PermissionHandler) PutPermission(w http.ResponseWriter, r *http.Request) {
	perm, ok := tablePermissionFromPath(w, r)
	if !ok {
		return
	}
	body, ok := readPermissionBody(w, r)
	if !ok {
		return
	}
	definition, err := permissions.NormalizePermission(perm.Operation, body)
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	perm.Definition = definition
	created, err := h.store.PutPermission(r.Context(), perm)
	if err != nil {
		h.saveFailed(w, perm.ProjectID, err)
		return
	}
	op := domain.OpChangeUpdate
	if created {
		op = domain.OpChangeCreate
	}
	h.publish(r.Context(), perm.ProjectID, domain.PermissionChangeKind, perm.Table, perm.Role+"/"+perm.Operation, op)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(definition)
}

func (h *PermissionHandler) DeletePermission(w http.ResponseWriter, r *http.Request) {
	perm, ok := tablePermissionFromPath(w, r)
	if !ok {
		return
	}
	err := h.store.DeletePermission(r.Context(), perm.ProjectID, perm.Table, perm.Role, perm.Operation)
	if errors.Is(err, storage.ErrPermissionNotFound) {
		httpError(w, errNotFound, http.StatusNotFound)
		return
	}
	if err != nil {
		h.saveFailed(w, perm.ProjectID, err)
		return
	}
	h.publish(r.Context(), perm.ProjectID, domain.PermissionChangeKind, perm.Table, perm.Role+"/"+perm.Operation,
		domain.OpChangeDelete)
	w.WriteHeader(http.StatusNoContent)
}

func readPermissionBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPermissionBody))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		httpError(w, "request body too large", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	if err != nil {
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return nil, false
	}
	return body, true
}

type trackFunctionRequest struct {
	Function         string  `json:"function"`
	InferPermissions *bool   `json:"inferPermissions"`
	SessionArgument  *string `json:"sessionArgument"`
}

type trackedFunctionAnswer struct {
	domain.TrackedFunction
	SecurityDefiner bool `json:"securityDefiner"`
}

// TrackFunction judges the function on its live definition: exposure follows
// volatility and is never taken from the request.
func (h *PermissionHandler) TrackFunction(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return
	}
	req, ok := decodeTrackRequest(w, r)
	if !ok {
		return
	}
	schemaName, name, _ := strings.Cut(req.Function, ".")
	details, err := h.inspector.FunctionDetails(r.Context(), projectID, schemaName, name)
	if err != nil {
		inspectFailed(w, projectID, req.Function, err)
		return
	}
	decision, err := permissions.Trackable(details, req.SessionArgument)
	if errors.Is(err, permissions.ErrFunctionNotFound) {
		httpError(w, "function "+req.Function+" not found in the project database", http.StatusBadRequest)
		return
	}
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	fn := domain.TrackedFunction{ProjectID: projectID, Function: req.Function, ExposedAs: decision.ExposedAs,
		InferPermissions: req.InferPermissions == nil || *req.InferPermissions, SessionArgument: req.SessionArgument}
	if err := h.store.TrackFunction(r.Context(), fn); err != nil {
		if errors.Is(err, storage.ErrFunctionAlreadyTracked) {
			httpError(w, "function "+req.Function+" is already tracked", http.StatusConflict)
			return
		}
		h.saveFailed(w, projectID, err)
		return
	}
	h.publish(r.Context(), projectID, domain.FunctionChangeKind, fn.Function, "", domain.OpChangeCreate)
	writeJSONStatus(w, http.StatusCreated, trackedFunctionAnswer{TrackedFunction: fn, SecurityDefiner: decision.SecurityDefiner})
}

func decodeTrackRequest(w http.ResponseWriter, r *http.Request) (trackFunctionRequest, bool) {
	var req trackFunctionRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPermissionBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		httpError(w, `body must be {"function": "schema.name", "inferPermissions": bool, "sessionArgument": `+
			`name or null}; exposedAs follows the function's volatility`, http.StatusBadRequest)
		return req, false
	}
	if err := validFunctionKey(req.Function); err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return req, false
	}
	return req, true
}

func validFunctionKey(function string) error {
	if err := permissions.ValidateQualifiedName(function); err != nil {
		return err
	}
	if schemaName, _, _ := strings.Cut(function, "."); !permissions.IsServedSchema(schemaName) {
		return errors.New("schema " + schemaName + " is not served by the API")
	}
	return nil
}

func inspectFailed(w http.ResponseWriter, projectID, function string, err error) {
	switch {
	case errors.Is(err, projectdb.ErrNotServable):
		httpError(w, "project is not running", http.StatusConflict)
	case errors.Is(err, domain.ErrNoDatabase):
		httpError(w, domain.ErrNoDatabase.Error(), http.StatusConflict)
	default:
		log.Printf("permissions: inspect function %s in %s: %v", function, projectID, err)
		httpError(w, "could not inspect the function in the project database", http.StatusBadGateway)
	}
}

func (h *PermissionHandler) UntrackFunction(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return
	}
	function := chi.URLParam(r, "function")
	if err := permissions.ValidateQualifiedName(function); err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	err := h.store.UntrackFunction(r.Context(), projectID, function)
	if errors.Is(err, storage.ErrFunctionNotTracked) {
		httpError(w, errNotFound, http.StatusNotFound)
		return
	}
	if err != nil {
		h.saveFailed(w, projectID, err)
		return
	}
	h.publish(r.Context(), projectID, domain.FunctionChangeKind, function, "", domain.OpChangeDelete)
	w.WriteHeader(http.StatusNoContent)
}

// functionPermissionFromPath validates the function and role segments.
func functionPermissionFromPath(w http.ResponseWriter, r *http.Request) (string, domain.FunctionPermission, bool) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return "", domain.FunctionPermission{}, false
	}
	fp := domain.FunctionPermission{Function: chi.URLParam(r, "function"), Role: chi.URLParam(r, "role")}
	for _, err := range []error{permissions.ValidateQualifiedName(fp.Function), permissions.ValidateRole(fp.Role)} {
		if err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return "", domain.FunctionPermission{}, false
		}
	}
	return projectID, fp, true
}

func (h *PermissionHandler) PutFunctionPermission(w http.ResponseWriter, r *http.Request) {
	projectID, fp, ok := functionPermissionFromPath(w, r)
	if !ok {
		return
	}
	err := h.store.PutFunctionPermission(r.Context(), projectID, fp.Function, fp.Role)
	if errors.Is(err, storage.ErrFunctionNotTracked) {
		httpError(w, "function "+fp.Function+" is not tracked", http.StatusBadRequest)
		return
	}
	if err != nil {
		h.saveFailed(w, projectID, err)
		return
	}
	h.publish(r.Context(), projectID, domain.FunctionChangeKind, fp.Function, fp.Role, domain.OpChangeUpdate)
	writeJSON(w, fp)
}

func (h *PermissionHandler) DeleteFunctionPermission(w http.ResponseWriter, r *http.Request) {
	projectID, fp, ok := functionPermissionFromPath(w, r)
	if !ok {
		return
	}
	err := h.store.DeleteFunctionPermission(r.Context(), projectID, fp.Function, fp.Role)
	if errors.Is(err, storage.ErrFunctionPermissionNotFound) {
		httpError(w, errNotFound, http.StatusNotFound)
		return
	}
	if err != nil {
		h.saveFailed(w, projectID, err)
		return
	}
	h.publish(r.Context(), projectID, domain.FunctionChangeKind, fp.Function, fp.Role, domain.OpChangeDelete)
	w.WriteHeader(http.StatusNoContent)
}

func (h *PermissionHandler) saveFailed(w http.ResponseWriter, projectID string, err error) {
	log.Printf("permissions: write for %s: %v", projectID, err)
	httpError(w, errPermissionsNotSaved, http.StatusInternalServerError)
}

func (h *PermissionHandler) publish(ctx context.Context, projectID, kind, resource, id, op string) {
	if h.publisher == nil {
		return
	}
	h.publisher.PublishPolicyChange(ctx, domain.PolicyChangeEvent{
		ProjectID: projectID, Kind: kind, Resource: resource, PolicyID: id, Op: op,
	})
}
