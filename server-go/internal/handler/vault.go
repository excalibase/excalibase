package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/go-chi/chi/v5"
)

const (
	errVaultSealed    = "vault is sealed"
	errSecretNotFound = "secret not found"
)

type VaultHandler struct {
	v *vault.Vault
	// instances and orgs resolve the project a secret path names against the
	// caller. main.go always wires both; without them there is no way to bind
	// the read, so every project secret is refused rather than served.
	instances storage.InstanceStore
	orgs      storage.OrgStore
}

func NewVaultHandler(v *vault.Vault) *VaultHandler {
	return &VaultHandler{v: v}
}

// SetInstanceStore wires the project lookup the secret route consults before
// handing out a project's credentials.
func (h *VaultHandler) SetInstanceStore(s storage.InstanceStore) { h.instances = s }

// SetOrgStore wires the membership lookup the project binding on secret reads
// consults.
func (h *VaultHandler) SetOrgStore(s storage.OrgStore) { h.orgs = s }

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

// projectIDForSecretPrefix is projectIDForSecret for a LIST prefix, where
// "projects/p1" names one project rather than being an incomplete path.
// "projects/" and "" name every project, so they resolve to "".
func projectIDForSecretPrefix(prefix string) string {
	rest, ok := strings.CutPrefix(prefix, vaultProjectPrefix)
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, "/")
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
		httpError(w, errSecretNotFound, http.StatusNotFound)
		return true
	}
	if inst == nil || domain.IsNotServable(inst.Status) {
		httpError(w, errSecretNotFound, http.StatusNotFound)
		return true
	}
	return false
}

// authorizeSecretRead binds a human caller's read to the project the secret
// path names and records that it happened. view_credentials answers what the
// caller's ROLE is, not which tenant they have anything to do with, so on its
// own it let any platform reader pull every tenant's database password
// (EXC-418) — and it cannot be repaired with another platform permission,
// because the only two roles holding view_credentials hold every read
// permission there is. The binding is the ordinary project resolution the rest
// of the platform uses, so platform_admin keeps the one documented bypass and
// platform_operator must be a member of the project's org like anyone else.
//
// Capability tokens are untouched: the gate above the route already names the
// one secret their service may read, and narrowing them here would break the
// principals that fetch tenant credentials for a living. They are still
// audited — they are the bulk of all reads.
func (h *VaultHandler) authorizeSecretRead(w http.ResponseWriter, r *http.Request, path string) bool {
	ctx := r.Context()
	token := auth.GetToken(ctx)
	user := auth.GetUser(ctx)
	if user == nil {
		httpError(w, "auth required", http.StatusUnauthorized)
		return false
	}
	projectID := projectIDForSecret(path)
	if !auth.IsCapabilityToken(token) && projectID != "" && !h.callerMaySeeProject(ctx, user, token, projectID) {
		httpError(w, errSecretNotFound, http.StatusNotFound)
		return false
	}
	logVaultAccess("vault.secret.read", user.ID, projectID, path)
	return true
}

// authorizeSecretList binds a prefix listing. A prefix under one project is
// that project's read; a prefix that spans projects is an inventory of every
// tenant the platform hosts, so it takes a platform admin on a session or an
// unrestricted token — the same bar the routes that mint platform-wide
// authority are held to. No capability token reaches this route — the
// capability gate names no listing — so there is no service exception here.
func (h *VaultHandler) authorizeSecretList(w http.ResponseWriter, r *http.Request, prefix string) bool {
	ctx := r.Context()
	token := auth.GetToken(ctx)
	user := auth.GetUser(ctx)
	if user == nil {
		httpError(w, "auth required", http.StatusUnauthorized)
		return false
	}
	projectID := projectIDForSecretPrefix(prefix)
	switch {
	case projectID == "":
		if !auth.HasPermission(user.Role, auth.PermManageUsers) || !auth.IsUnrestrictedCredential(token) {
			httpError(w, "insufficient permissions", http.StatusForbidden)
			return false
		}
	case !h.callerMaySeeProject(ctx, user, token, projectID):
		httpError(w, errSecretNotFound, http.StatusNotFound)
		return false
	}
	logVaultAccess("vault.secret.list", user.ID, projectID, prefix)
	return true
}

// callerMaySeeProject answers whether this caller reaches projectID, using the
// same resolution every other project-bound route runs. Without the stores
// there is no way to answer, so the door stays shut.
func (h *VaultHandler) callerMaySeeProject(ctx context.Context, user *domain.User, token *domain.AccessToken, projectID string) bool {
	if h.instances == nil || h.orgs == nil {
		return false
	}
	return custommw.ResolveProjectAccess(ctx, user, token, projectID, h.instances, h.orgs) != nil
}

// logVaultAccess records who reached which secret or prefix. The value is
// never logged — the point of the line is attribution, and a log that carries
// the credential is a second copy of it.
func logVaultAccess(event, callerID, projectID, path string) {
	log.Printf("INFO: %s caller=%s project=%s path=%s", event, callerID, projectID, path)
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
	if !h.authorizeSecretList(w, r, prefix) {
		return
	}
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
	if !h.authorizeSecretRead(w, r, path) {
		return
	}
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
			httpError(w, errSecretNotFound, http.StatusNotFound)
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
