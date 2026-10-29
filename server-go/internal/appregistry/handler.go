package appregistry

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"
)

const (
	// pullTokenTTL is how long a node's pull token lasts; containerd asks again per pull.
	pullTokenTTL = 2 * time.Minute
	maxScopes    = 16
	maxFormBytes = 16 << 10
)

// ProjectExists answers whether a project still exists; a deleted one's derived password opens nothing.
type ProjectExists func(ctx context.Context, projectID string) (bool, error)

// TokenHandler is the registry's token service (the Docker token spec), which
// zot reaches through its upstream token endpoint. A project's pull
// credential gets pull, and only on its own repositories; push and delete
// tokens are minted for our own steps and never handed out here.
type TokenHandler struct {
	issuer   *Issuer
	internal *Internal
	exists   ProjectExists
}

// NewTokenHandler serves an issuing registry; one that only guards has no token service.
func NewTokenHandler(internal *Internal, exists ProjectExists) (*TokenHandler, error) {
	if !internal.CanIssue() {
		return nil, errNotIssuing
	}
	return &TokenHandler{issuer: internal.issuer, internal: internal, exists: exists}, nil
}

func (h *TokenHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if r.Form.Get("service") != h.issuer.service {
		http.Error(w, "unknown service", http.StatusBadRequest)
		return
	}
	var scopes []string
	for _, field := range r.Form["scope"] {
		scopes = append(scopes, strings.Fields(field)...)
	}
	if len(scopes) > maxScopes {
		http.Error(w, "too many scopes", http.StatusBadRequest)
		return
	}
	projectID, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	token, err := h.issuer.Mint(projectID, pullGrants(projectID, scopes), pullTokenTTL)
	if err != nil {
		log.Printf("registry token for %s: %v", projectID, err)
		http.Error(w, "could not issue a token", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token": token, "access_token": token, "expires_in": int(pullTokenTTL.Seconds()),
		"issued_at": h.issuer.now().UTC().Format(time.RFC3339),
	})
}

// authenticate takes Basic credentials (the token spec's GET) or the OAuth2 password form (POST).
func (h *TokenHandler) authenticate(w http.ResponseWriter, r *http.Request) (string, bool) {
	user, password, ok := r.BasicAuth()
	if !ok && r.Method == http.MethodPost && r.PostForm.Get("grant_type") == "password" {
		user, password, ok = r.PostForm.Get("username"), r.PostForm.Get("password"), true
	}
	if !ok || !segment.MatchString(user) {
		unauthorized(w)
		return "", false
	}
	verified, err := h.internal.VerifyPull(user, password)
	if err != nil {
		log.Printf("registry token: read %s's pull generation: %v", user, err)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return "", false
	}
	if !verified {
		log.Printf("registry token: refused a pull credential for %s", user)
		unauthorized(w)
		return "", false
	}
	exists, err := h.exists(r.Context(), user)
	if err != nil {
		log.Printf("registry token: look up project %s: %v", user, err)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return "", false
	}
	if !exists {
		unauthorized(w)
		return "", false
	}
	return user, true
}

func unauthorized(w http.ResponseWriter) {
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// pullGrants keeps the pull action on the project's own repositories; anything else asked for is left out, as the spec allows.
func pullGrants(projectID string, scopes []string) []Grant {
	var grants []Grant
	for _, scope := range scopes {
		parts := strings.Split(scope, ":")
		if len(parts) != 3 || parts[0] != "repository" || !OwnedBy(parts[1], projectID) {
			continue
		}
		if slices.Contains(strings.Split(parts[2], ","), "pull") {
			grants = append(grants, Grant{Type: "repository", Name: parts[1], Actions: []string{"pull"}})
		}
	}
	return grants
}
