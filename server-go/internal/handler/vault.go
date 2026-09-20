package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/go-chi/chi/v5"
)

const (
	errVaultSealed = "vault is sealed"
)

type VaultHandler struct {
	v *vault.Vault
	// instances gates secrets filed under a project. Optional: without it
	// project secrets are served as before, which is why main.go wires it.
	instances storage.InstanceStore
}

func NewVaultHandler(v *vault.Vault) *VaultHandler {
	return &VaultHandler{v: v}
}

// SetInstanceStore wires the project lookup the secret route consults before
// handing out a project's credentials.
func (h *VaultHandler) SetInstanceStore(s storage.InstanceStore) { h.instances = s }

// vaultProjectPrefix is the path every project's secrets are filed under.
const vaultProjectPrefix = "projects/"

// projectIDForSecret returns the project a secret path belongs to, or "" for
// a path that is not a project's (pki/…, backup/…).
func projectIDForSecret(path string) string {
	rest, ok := strings.CutPrefix(path, vaultProjectPrefix)
	if !ok {
		return ""
	}
	id, _, found := strings.Cut(rest, "/")
	if !found {
		return ""
	}
	return id
}

// refuseSecretOfUnservableProject answers 404 when the secret belongs to a
// project the platform must not serve. 404 rather than 409 on purpose: the
// callers here are services (svc-auth, svc-graphql) fetching tenant database
// credentials, and the only correct reaction is to treat the project as
// absent and stop serving it — not to retry.
//
// The lookup costs one indexed read by primary key, and these services hold
// the result on a cache TTL rather than fetching per request, so this is not
// on a per-request hot path.
func (h *VaultHandler) refuseSecretOfUnservableProject(w http.ResponseWriter, path string) bool {
	if h.instances == nil {
		return false
	}
	projectID := projectIDForSecret(path)
	if projectID == "" {
		return false
	}
	inst, err := h.instances.FindByProjectID(projectID)
	if err != nil {
		httpError(w, "secret not found", http.StatusNotFound)
		return true
	}
	if inst == nil || domain.IsNotServable(inst.Status) {
		httpError(w, "secret not found", http.StatusNotFound)
		return true
	}
	return false
}

func (h *VaultHandler) Routes(r chi.Router) {
	// Public read-only routes. /status backs the first-run setup UI (which polls
	// it before any user exists) and the PKI public key is public by design.
	r.Get("/status", h.Status)
	r.Get("/pki/public-key", h.GetPublicKey)

	// Auth-required routes. Beyond authentication, every secret-touching route
	// requires PermViewCredentials — a platform-level permission held by
	// platform_admin / platform_operator (and the provisioning PAT the GraphQL
	// engine + internal services authenticate with) but NOT by a normal
	// dashboard tenant ("user") or platform_viewer.
	//
	// init / unseal / rekey / seal are lifecycle operations that create or
	// consume the master key material: /init returns the unseal shares, so an
	// unauthenticated caller who reaches it first owns the vault (SEC-C1). Like
	// HashiCorp Vault, these require an operator credential. The bootstrap Job
	// registers the first admin, logs in for a PAT, then calls these with that
	// PAT (PAT auth is validated against the platform DB, so it works even while
	// the vault is sealed) — and the setup UI registers the admin before the
	// vault steps for the same reason.
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth)
		r.Use(auth.RequirePermission(auth.PermViewCredentials))
		// PermViewCredentials asks what the caller's role is, not what their
		// credential was narrowed to, so an operator's project-bound PAT
		// satisfied it and could seal, rekey or overwrite platform-wide key
		// material. Reads stay open — the service principals fetch one named
		// secret with a capability token (EXC-395).
		r.Use(auth.RequireUnrestrictedCredentialForWrites)
		r.Post("/init", h.Init)
		r.Post("/unseal", h.Unseal)
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
		httpError(w, errInvalidBody, http.StatusBadRequest)
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
		httpError(w, errInvalidBody, http.StatusBadRequest)
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
			httpError(w, errVaultSealed, http.StatusServiceUnavailable)
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
			httpError(w, errVaultSealed, http.StatusServiceUnavailable)
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
	if h.refuseSecretOfUnservableProject(w, path) {
		return
	}
	data, err := h.v.Get(path)
	if err != nil {
		if errors.Is(err, vault.ErrSealed) {
			httpError(w, errVaultSealed, http.StatusServiceUnavailable)
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
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return
	}

	if err := h.v.Put(path, data); err != nil {
		if errors.Is(err, vault.ErrSealed) {
			httpError(w, errVaultSealed, http.StatusServiceUnavailable)
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
			httpError(w, errVaultSealed, http.StatusServiceUnavailable)
			return
		}
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// DeletePrefix removes every secret under ?prefix=... in one transaction.
// Empty prefix is rejected so we can't accidentally wipe the vault.
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
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"deleted": deleted})
}

func extractSecretPath(r *http.Request) string {
	// chi wildcard gives us everything after /secrets/
	path := chi.URLParam(r, "*")
	path = strings.TrimPrefix(path, "/")
	return path
}
