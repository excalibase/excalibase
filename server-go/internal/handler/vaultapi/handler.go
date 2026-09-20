package vaultapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/go-chi/chi/v5"
)

const (
	errInvalidBody = "invalid request body"
	errVaultSealed = "vault is sealed"
	errInternal    = "internal error"
)

type VaultHandler struct {
	v *vault.Vault
}

func NewVaultHandler(v *vault.Vault) *VaultHandler {
	return &VaultHandler{v: v}
}

// Routes registers all vault API routes. tokens MUST be non-empty — passing
// an empty list will panic. The standalone vault service has no other auth
// surface; running it open would expose every secret to anything that can
// reach the port. Init/Unseal remain public by design (they can't require
// pre-existing auth, and Init refuses if the vault is already initialised),
// but every secret operation and admin operation is PAT-gated.
func (h *VaultHandler) Routes(r chi.Router, tokens []string) {
	if len(tokens) == 0 {
		panic("vaultapi: at least one PAT must be configured")
	}
	// Public
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	r.Get("/status", h.Status)
	r.Post("/init", h.Init)     // self-gated: refuses if already initialised
	r.Post("/unseal", h.Unseal) // self-gated: requires a valid Shamir share
	r.Get("/pki/public-key", h.GetPublicKey)

	// Auth-gated: every secret op + the admin lifecycle ops.
	r.Group(func(r chi.Router) {
		r.Use(requirePAT(tokens))
		r.Post("/seal", h.Seal)
		r.Post("/rekey", h.Rekey)
		r.Get("/secrets-list", h.ListSecrets)
		r.Delete("/secrets-list", h.DeletePrefix)
		r.Route("/secrets", func(r chi.Router) {
			r.Get("/*", h.GetSecret)
			r.Put("/*", h.PutSecret)
			r.Delete("/*", h.DeleteSecret)
		})
	})
}

func requirePAT(validTokens []string) func(http.Handler) http.Handler {
	// Pre-compute byte slices once for constant-time comparison.
	tokenBytes := make([][]byte, len(validTokens))
	for i, t := range validTokens {
		tokenBytes[i] = []byte(t)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				httpError(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			candidate := []byte(strings.TrimPrefix(auth, "Bearer "))
			matched := 0
			for _, t := range tokenBytes {
				// ConstantTimeCompare returns 0 on length mismatch or content
				// mismatch; OR-ing the result accumulates a 1 on any match
				// without short-circuiting, so total time is O(len(tokens)).
				matched |= subtle.ConstantTimeCompare(candidate, t)
			}
			if matched == 0 {
				httpError(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (h *VaultHandler) Status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"initialized": h.v.Initialized(),
		"sealed":      h.v.Sealed(),
	})
}

func (h *VaultHandler) Init(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Shares    int `json:"shares"`
		Threshold int `json:"threshold"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, errInvalidBody, http.StatusBadRequest)
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
		httpError(w, err.Error(), http.StatusBadRequest)
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
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return
	}

	progress, err := h.v.Unseal(body.Share)
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
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
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return
	}

	result, err := h.v.Rekey(body.Shares, body.Threshold)
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
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
			httpError(w, errVaultSealed, http.StatusServiceUnavailable)
			return
		}
		if errors.Is(err, vault.ErrNotFound) {
			httpError(w, "PKI not initialized", http.StatusNotFound)
			return
		}
		httpError(w, errInternal, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"key": pem, "algorithm": "EC-P256"})
}

func (h *VaultHandler) ListSecrets(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	paths, err := h.v.List(prefix)
	if err != nil {
		if errors.Is(err, vault.ErrSealed) {
			httpError(w, errVaultSealed, http.StatusServiceUnavailable)
			return
		}
		httpError(w, errInternal, http.StatusInternalServerError)
		return
	}
	if paths == nil {
		paths = []string{}
	}
	writeJSON(w, map[string]interface{}{"paths": paths})
}

func (h *VaultHandler) GetSecret(w http.ResponseWriter, r *http.Request) {
	path := extractPath(r)
	data, err := h.v.Get(path)
	if err != nil {
		if errors.Is(err, vault.ErrSealed) {
			httpError(w, errVaultSealed, http.StatusServiceUnavailable)
			return
		}
		if errors.Is(err, vault.ErrNotFound) {
			httpError(w, "not found", http.StatusNotFound)
			return
		}
		httpError(w, errInternal, http.StatusInternalServerError)
		return
	}
	writeJSON(w, data)
}

func (h *VaultHandler) PutSecret(w http.ResponseWriter, r *http.Request) {
	path := extractPath(r)
	var data map[string]string
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return
	}

	if err := h.v.Put(path, data); err != nil {
		if errors.Is(err, vault.ErrSealed) {
			httpError(w, errVaultSealed, http.StatusServiceUnavailable)
			return
		}
		httpError(w, errInternal, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *VaultHandler) DeleteSecret(w http.ResponseWriter, r *http.Request) {
	path := extractPath(r)
	if err := h.v.Delete(path); err != nil {
		if errors.Is(err, vault.ErrSealed) {
			httpError(w, errVaultSealed, http.StatusServiceUnavailable)
			return
		}
		httpError(w, errInternal, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *VaultHandler) DeletePrefix(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	if prefix == "" {
		httpError(w, "prefix is required", http.StatusBadRequest)
		return
	}
	deleted, err := h.v.DeletePrefix(prefix)
	if err != nil {
		if errors.Is(err, vault.ErrSealed) {
			httpError(w, errVaultSealed, http.StatusServiceUnavailable)
			return
		}
		httpError(w, errInternal, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"deleted": deleted})
}

func extractPath(r *http.Request) string {
	return strings.TrimPrefix(chi.URLParam(r, "*"), "/")
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]interface{}{"error": msg, "status": code})
}
