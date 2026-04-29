package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/go-chi/chi/v5"
)

type VaultHandler struct {
	v *vault.Vault
}

func NewVaultHandler(v *vault.Vault) *VaultHandler {
	return &VaultHandler{v: v}
}

func (h *VaultHandler) Routes(r chi.Router) {
	// Public routes (no auth required)
	r.Get("/status", h.Status)
	r.Post("/init", h.Init)
	r.Post("/unseal", h.Unseal)
	r.Get("/pki/public-key", h.GetPublicKey)

	// Auth-required routes
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth)
		r.Post("/seal", h.Seal)
		r.Post("/rekey", h.Rekey)
		r.Get("/secrets-list", h.ListSecrets)
		r.Route("/secrets", func(r chi.Router) {
			r.Get("/*", h.GetSecret)
			r.Put("/*", h.PutSecret)
			r.Delete("/*", h.DeleteSecret)
		})
	})
}

func (h *VaultHandler) Status(w http.ResponseWriter, r *http.Request) {
	s := h.v.Status()
	writeJSON(w, map[string]interface{}{
		"initialized": s.Initialized,
		"sealed":      s.Sealed,
		"threshold":   s.Threshold,
		"shares":      s.Shares,
		"progress":    s.Progress,
		"type":        s.Type,
	})
}

func (h *VaultHandler) Init(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Shares    int `json:"shares"`
		Threshold int `json:"threshold"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.Shares < 1 {
		body.Shares = 5
	}
	if body.Threshold < 1 {
		body.Threshold = 3
	}

	result, err := h.v.Init(body.Shares, body.Threshold)
	if err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}

	writeJSON(w, map[string]interface{}{
		"shares":    result.Shares,
		"threshold": result.Threshold,
	})
}

func (h *VaultHandler) Unseal(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Share string `json:"share"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	progress, err := h.v.Unseal(body.Share)
	if err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}

	writeJSON(w, map[string]interface{}{
		"sealed":    !progress.Done,
		"progress":  progress.Progress,
		"threshold": progress.Threshold,
	})
}

func (h *VaultHandler) Seal(w http.ResponseWriter, r *http.Request) {
	h.v.Seal()
	writeJSON(w, map[string]interface{}{"sealed": true})
}

func (h *VaultHandler) Rekey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Shares    int `json:"shares"`
		Threshold int `json:"threshold"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	result, err := h.v.Rekey(body.Shares, body.Threshold)
	if err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}

	writeJSON(w, map[string]interface{}{
		"shares":    result.Shares,
		"threshold": result.Threshold,
	})
}

func (h *VaultHandler) GetPublicKey(w http.ResponseWriter, r *http.Request) {
	pem, err := h.v.GetPublicKey()
	if err != nil {
		if errors.Is(err, vault.ErrSealed) {
			httpError(w, "vault is sealed", http.StatusServiceUnavailable)
			return
		}
		if errors.Is(err, vault.ErrNotFound) {
			httpError(w, "PKI not initialized", http.StatusNotFound)
			return
		}
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"key": pem, "algorithm": "EC-P256"})
}

func (h *VaultHandler) ListSecrets(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	paths, err := h.v.List(prefix)
	if err != nil {
		if errors.Is(err, vault.ErrSealed) {
			httpError(w, "vault is sealed", http.StatusServiceUnavailable)
			return
		}
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	if paths == nil {
		paths = []string{}
	}
	writeJSON(w, map[string]interface{}{"paths": paths})
}

func (h *VaultHandler) GetSecret(w http.ResponseWriter, r *http.Request) {
	path := extractSecretPath(r)
	data, err := h.v.Get(path)
	if err != nil {
		if errors.Is(err, vault.ErrSealed) {
			httpError(w, "vault is sealed", http.StatusServiceUnavailable)
			return
		}
		if errors.Is(err, vault.ErrNotFound) {
			httpError(w, "secret not found", http.StatusNotFound)
			return
		}
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, data)
}

func (h *VaultHandler) PutSecret(w http.ResponseWriter, r *http.Request) {
	path := extractSecretPath(r)
	var data map[string]string
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := h.v.Put(path, data); err != nil {
		if errors.Is(err, vault.ErrSealed) {
			httpError(w, "vault is sealed", http.StatusServiceUnavailable)
			return
		}
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *VaultHandler) DeleteSecret(w http.ResponseWriter, r *http.Request) {
	path := extractSecretPath(r)
	if err := h.v.Delete(path); err != nil {
		if errors.Is(err, vault.ErrSealed) {
			httpError(w, "vault is sealed", http.StatusServiceUnavailable)
			return
		}
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func extractSecretPath(r *http.Request) string {
	// chi wildcard gives us everything after /secrets/
	path := chi.URLParam(r, "*")
	path = strings.TrimPrefix(path, "/")
	return path
}
