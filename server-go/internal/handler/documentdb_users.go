package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

// A DocumentDB project's own Mongo users (EXC-427). Mounted behind Admin+:
// create and rotate answer a password, once.

// MongoUserRoutes mounts the collection under /documentdb/users.
func (h *ProvisioningHandler) MongoUserRoutes(r chi.Router) {
	r.Get("/", h.ListMongoUsers)
	r.Post("/", h.CreateMongoUser)
	r.Post("/{username}/rotate", h.RotateMongoUser)
	r.Delete("/{username}", h.DeleteMongoUser)
}

type createMongoUserRequest struct {
	Username string                `json:"username"`
	Role     service.MongoUserRole `json:"role"`
}

func (h *ProvisioningHandler) ListMongoUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.svc.ListMongoUsers(r.Context(), chi.URLParam(r, "projectId"))
	if err != nil {
		writeMongoUserError(w, err)
		return
	}
	writeJSON(w, map[string]interface{}{"users": users, "limit": service.MaxMongoUsersPerProject})
}

func (h *ProvisioningHandler) CreateMongoUser(w http.ResponseWriter, r *http.Request) {
	var req createMongoUserRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		httpError(w, errInvalidRequestBody, http.StatusBadRequest)
		return
	}
	cred, err := h.svc.CreateMongoUser(r.Context(), chi.URLParam(r, "projectId"), req.Username, req.Role)
	if err != nil {
		writeMongoUserError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSONStatus(w, http.StatusCreated, cred)
}

func (h *ProvisioningHandler) RotateMongoUser(w http.ResponseWriter, r *http.Request) {
	cred, err := h.svc.RotateMongoUser(r.Context(), chi.URLParam(r, "projectId"), chi.URLParam(r, "username"))
	if err != nil {
		writeMongoUserError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, cred)
}

func (h *ProvisioningHandler) DeleteMongoUser(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteMongoUser(r.Context(), chi.URLParam(r, "projectId"), chi.URLParam(r, "username")); err != nil {
		writeMongoUserError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeMongoUserError answers a refusal in its own words; anything else is
// logged and answered without the database's detail.
func writeMongoUserError(w http.ResponseWriter, err error) {
	for _, refusal := range []struct {
		err    error
		status int
	}{
		{service.ErrInvalidMongoUsername, http.StatusBadRequest},
		{service.ErrInvalidMongoUserRole, http.StatusBadRequest},
		{service.ErrMongoUserNotFound, http.StatusNotFound},
		{service.ErrMongoUserExists, http.StatusConflict},
		{service.ErrMongoUserLimit, http.StatusConflict},
		{service.ErrNotDocumentDBProject, http.StatusConflict},
		{service.ErrMongoUsersUnavailable, http.StatusServiceUnavailable},
	} {
		if errors.Is(err, refusal.err) {
			httpError(w, refusal.err.Error(), refusal.status)
			return
		}
	}
	if status := unsettledOr(err, 0); status != 0 {
		httpError(w, safeError(err), status)
		return
	}
	log.Printf("Mongo users: %s", safeLog(err.Error()))
	httpError(w, "the Mongo user change could not be completed; try again", http.StatusInternalServerError)
}
