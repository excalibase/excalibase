package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/go-chi/chi/v5"
)

type VaultHandler struct {
	v *vault.Vault
}

func NewVaultHandler(v *vault.Vault) *VaultHandler {
	return &VaultHandler{v: v}
}

func (h *VaultHandler) Routes(r chi.Router, tokens []string) {
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	// Mount vault routes at root and /vault/ for backward compat with auth/graphql
	mountVaultRoutes := func(r chi.Router) {
		r.Get("/status", h.Status)
		r.Post("/init", h.Init)
		r.Post("/unseal", h.Unseal)
		r.Get("/pki/public-key", h.GetPublicKey)

		r.Group(func(r chi.Router) {
			if len(tokens) > 0 {
				r.Use(requirePAT(tokens))
			}
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

	mountVaultRoutes(r)
	r.Route("/vault", func(r chi.Router) {
		mountVaultRoutes(r)
	})
}

func requirePAT(validTokens []string) func(http.Handler) http.Handler {
	tokenSet := make(map[string]struct{}, len(validTokens))
	for _, t := range validTokens {
		tokenSet[t] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				httpError(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			token := strings.TrimPrefix(auth, "Bearer ")
			if _, ok := tokenSet[token]; !ok {
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
		httpError(w, "invalid request body", http.StatusBadRequest)
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
		httpError(w, "invalid request body", http.StatusBadRequest)
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
			httpError(w, "vault is sealed", http.StatusServiceUnavailable)
			return
		}
		if errors.Is(err, vault.ErrNotFound) {
			httpError(w, "PKI not initialized", http.StatusNotFound)
			return
		}
		httpError(w, "internal error", http.StatusInternalServerError)
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
		httpError(w, "internal error", http.StatusInternalServerError)
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
			httpError(w, "vault is sealed", http.StatusServiceUnavailable)
			return
		}
		if errors.Is(err, vault.ErrNotFound) {
			httpError(w, "not found", http.StatusNotFound)
			return
		}
		httpError(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, data)
}

func (h *VaultHandler) PutSecret(w http.ResponseWriter, r *http.Request) {
	path := extractPath(r)
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
		httpError(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *VaultHandler) DeleteSecret(w http.ResponseWriter, r *http.Request) {
	path := extractPath(r)
	if err := h.v.Delete(path); err != nil {
		if errors.Is(err, vault.ErrSealed) {
			httpError(w, "vault is sealed", http.StatusServiceUnavailable)
			return
		}
		httpError(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
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
