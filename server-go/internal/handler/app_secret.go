package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
	"github.com/go-chi/chi/v5"
)

// maxSecretBodyBytes leaves room for the largest value plus its JSON framing.
const maxSecretBodyBytes = 2 * apphost.MaxLiteralValueLength

const errReadApp = "could not read the app"

// AppSecretHandler stores the values of an app's secret variables. It is
// write-only: the value goes to the project's vault and no response, here or
// anywhere else, carries it back.
type AppSecretHandler struct {
	store apphost.Store
	vault vaultclient.VaultClient
}

func NewAppSecretHandler(store apphost.Store, vault vaultclient.VaultClient) *AppSecretHandler {
	return &AppSecretHandler{store: store, vault: vault}
}

type appSecretRequest struct {
	Value *string `json:"value"`
}

// Set writes the value, then points the variable at it. The vault is written
// first so a variable never points at a value that was not stored.
func (h *AppSecretHandler) Set(w http.ResponseWriter, r *http.Request) {
	projectID, appID, name, ok := h.secretPath(w, r)
	if !ok {
		return
	}
	value, ok := readSecretValue(w, r)
	if !ok {
		return
	}
	if h.vault == nil {
		httpError(w, "secrets are unavailable: no vault is configured", http.StatusServiceUnavailable)
		return
	}
	app, err := h.store.Get(projectID, appID)
	if err != nil {
		httpError(w, errReadApp, http.StatusInternalServerError)
		return
	}
	if app == nil {
		httpError(w, errNotFound, http.StatusNotFound)
		return
	}

	readVersion := app.Version
	ref := app.SetSecretVar(name)
	if err := app.Validate(); err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.vault.Put(ref.Path, map[string]string{ref.Key: value}); err != nil {
		httpError(w, "could not store the secret", http.StatusInternalServerError)
		return
	}
	if err := h.store.Update(app, readVersion); err != nil {
		h.writeStoreError(w, err)
		return
	}
	w.Header().Set("ETag", strconv.Itoa(app.Version))
	writeJSON(w, map[string]any{"name": name, "set": true})
}

func (h *AppSecretHandler) secretPath(w http.ResponseWriter, r *http.Request) (string, string, string, bool) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return "", "", "", false
	}
	appID := chi.URLParam(r, "appId")
	if err := apphost.ValidateID(appID); err != nil {
		httpError(w, "invalid appId", http.StatusBadRequest)
		return "", "", "", false
	}
	name := chi.URLParam(r, "name")
	if err := apphost.ValidateEnvName(name); err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return "", "", "", false
	}
	return projectID, appID, name, true
}

func readSecretValue(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req appSecretRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxSecretBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return "", false
	}
	switch {
	case req.Value == nil || *req.Value == "":
		httpError(w, "value is required", http.StatusBadRequest)
		return "", false
	case len(*req.Value) > apphost.MaxLiteralValueLength:
		httpError(w, "value exceeds "+strconv.Itoa(apphost.MaxLiteralValueLength)+" bytes", http.StatusBadRequest)
		return "", false
	}
	return *req.Value, true
}

func (h *AppSecretHandler) writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, apphost.ErrAppNotFound):
		httpError(w, errNotFound, http.StatusNotFound)
	case errors.Is(err, apphost.ErrAppVersionConflict):
		httpError(w, "the app was changed by someone else; try again", http.StatusPreconditionFailed)
	default:
		httpError(w, "could not save the app", http.StatusInternalServerError)
	}
}
