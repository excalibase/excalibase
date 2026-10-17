// Package githubfake is an in-process GitHub for git-build tests: the App
// API (JWT-authenticated installation tokens), the user sign-in flow, the
// repository reads a build makes, and signed webhook deliveries.
package githubfake

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	AppID        = int64(4242)
	AppSlug      = "excalibase-sites-test"
	ClientID     = "Iv1.testclient"
	ClientSecret = "test-client-secret"
)

// Repo is one repository the fake serves.
type Repo struct {
	ID            int64
	FullName      string
	DefaultBranch string
	Private       bool
	// Branches maps a branch to its head commit sha.
	Branches map[string]string
}

// TokenRequest is one installation token minted, as asked for.
type TokenRequest struct {
	InstallationID int64
	RepositoryIDs  []int64
	Permissions    map[string]string
	Token          string
}

// GitHub is the fake. Its methods are safe for concurrent use.
type GitHub struct {
	Server        *httptest.Server
	PrivateKeyPEM string
	WebhookSecret string

	key *rsa.PrivateKey
	mu  sync.Mutex
	// installations maps an installation to its account and repositories.
	installations map[int64]installation
	// users maps a sign-in code to the installations and repos that user sees.
	codes         map[string]map[int64][]int64
	issued        []TokenRequest
	revokedTokens []string
	revokedUsers  []string
	userTokens    map[string]map[int64][]int64
}

type installation struct {
	account string
	repos   map[int64]*Repo
}

// New starts a fake GitHub that closes with the test.
const bearerPrefix = "Bearer "

// badCredentials is GitHub's own answer to a token it does not accept.
var badCredentials = map[string]string{"message": "Bad credentials"}

func New(t testing.TB) *GitHub {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	fake := &GitHub{
		PrivateKeyPEM: string(encoded), WebhookSecret: "fake-webhook-secret", key: key,
		installations: map[int64]installation{}, codes: map[string]map[int64][]int64{},
		userTokens: map[string]map[int64][]int64{},
	}
	fake.Server = httptest.NewServer(fake.routes())
	t.Cleanup(fake.Server.Close)
	return fake
}

// URL is both the API and the web base of the fake.
func (g *GitHub) URL() string { return g.Server.URL }

// AddInstallation makes repos reachable through installation id, on account.
func (g *GitHub) AddInstallation(id int64, account string, repos ...*Repo) {
	g.mu.Lock()
	defer g.mu.Unlock()
	held := map[int64]*Repo{}
	for _, repo := range repos {
		held[repo.ID] = repo
	}
	g.installations[id] = installation{account: account, repos: held}
}

// AddSignIn makes code sign in a user who sees the given repos per installation.
func (g *GitHub) AddSignIn(code string, visible map[int64][]int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.codes[code] = visible
}

// SetBranchHead moves a branch of a repo.
func (g *GitHub) SetBranchHead(repoID int64, branch, sha string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, inst := range g.installations {
		if repo, ok := inst.repos[repoID]; ok {
			repo.Branches[branch] = sha
		}
	}
}

// RenameRepo gives a repo a new owner/name, as a transfer or rename does; its id stays.
func (g *GitHub) RenameRepo(repoID int64, fullName string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, inst := range g.installations {
		if repo, ok := inst.repos[repoID]; ok {
			repo.FullName = fullName
		}
	}
}

// Issued is every installation token minted so far.
func (g *GitHub) Issued() []TokenRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]TokenRequest(nil), g.issued...)
}

// Revoked is every installation token revoked so far.
func (g *GitHub) Revoked() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.revokedTokens...)
}

// RevokedUserTokens is every user token the platform gave back.
func (g *GitHub) RevokedUserTokens() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.revokedUsers...)
}

// Sign answers the X-Hub-Signature-256 header GitHub would send for body.
func (g *GitHub) Sign(body []byte) string {
	mac := hmac.New(sha256.New, []byte(g.WebhookSecret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// PushPayload is a push event body as GitHub sends it.
func PushPayload(installationID, repoID int64, fullName, branch, sha, message string) []byte {
	body, _ := json.Marshal(map[string]any{
		"ref": "refs/heads/" + branch, "after": sha, "deleted": false,
		"installation": map[string]any{"id": installationID},
		"repository":   map[string]any{"id": repoID, "full_name": fullName},
		"head_commit":  map[string]any{"id": sha, "message": message},
	})
	return body
}

func (g *GitHub) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", g.accessToken)
	mux.HandleFunc("DELETE /installation/token", g.revokeInstallationToken)
	mux.HandleFunc("GET /repositories/{id}", g.repository)
	mux.HandleFunc("GET /repos/{owner}/{repo}/git/ref/heads/{branch...}", g.branchRef)
	mux.HandleFunc("GET /repos/{owner}/{repo}/commits/{sha}", g.commit)
	mux.HandleFunc("POST /login/oauth/access_token", g.exchangeCode)
	mux.HandleFunc("GET /user/installations", g.userInstallations)
	mux.HandleFunc("GET /user/installations/{id}/repositories", g.userRepositories)
	mux.HandleFunc("DELETE /applications/{client}/token", g.revokeUserToken)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func notFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
}

// appAuthorized checks the App JWT the way GitHub does: RS256 by the App's key, iss = app id, short-lived.
func (g *GitHub) appAuthorized(r *http.Request) bool {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), bearerPrefix)
	if !ok {
		return false
	}
	claims := jwt.RegisteredClaims{}
	token, err := jwt.ParseWithClaims(raw, &claims, func(token *jwt.Token) (any, error) {
		return &g.key.PublicKey, nil
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(strconv.FormatInt(AppID, 10)))
	if err != nil || !token.Valid || claims.ExpiresAt == nil || claims.IssuedAt == nil {
		return false
	}
	return claims.ExpiresAt.Sub(claims.IssuedAt.Time) <= 10*time.Minute
}

func (g *GitHub) accessToken(w http.ResponseWriter, r *http.Request) {
	if !g.appAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "A JSON web token could not be decoded"})
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var body struct {
		RepositoryIDs []int64           `json:"repository_ids"`
		Permissions   map[string]string `json:"permissions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "bad body"})
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	inst, ok := g.installations[id]
	if !ok {
		notFound(w)
		return
	}
	for _, repoID := range body.RepositoryIDs {
		if _, held := inst.repos[repoID]; !held {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "There is at least one repository that does not exist or is not accessible"})
			return
		}
	}
	token := fmt.Sprintf("ghs_fake_%d_%d", id, len(g.issued)+1)
	g.issued = append(g.issued, TokenRequest{InstallationID: id, RepositoryIDs: body.RepositoryIDs, Permissions: body.Permissions, Token: token})
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
}

// tokenScope answers the repos an installation token reaches, or false for an unknown token.
func (g *GitHub) tokenScope(r *http.Request) (installation, []int64, bool) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), bearerPrefix)
	if !ok {
		raw, ok = strings.CutPrefix(r.Header.Get("Authorization"), "token ")
	}
	if !ok {
		return installation{}, nil, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, revoked := range g.revokedTokens {
		if revoked == raw {
			return installation{}, nil, false
		}
	}
	for _, issued := range g.issued {
		if issued.Token == raw {
			return g.installations[issued.InstallationID], issued.RepositoryIDs, true
		}
	}
	return installation{}, nil, false
}

func (g *GitHub) revokeInstallationToken(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := g.tokenScope(r); !ok {
		writeJSON(w, http.StatusUnauthorized, badCredentials)
		return
	}
	g.mu.Lock()
	g.revokedTokens = append(g.revokedTokens, strings.TrimPrefix(r.Header.Get("Authorization"), bearerPrefix))
	g.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (g *GitHub) scopedRepo(r *http.Request, match func(*Repo) bool) (*Repo, bool) {
	inst, scope, ok := g.tokenScope(r)
	if !ok {
		return nil, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, id := range scope {
		if repo, held := inst.repos[id]; held && match(repo) {
			return repo, true
		}
	}
	return nil, false
}

func repoJSON(repo *Repo) map[string]any {
	return map[string]any{"id": repo.ID, "full_name": repo.FullName, "default_branch": repo.DefaultBranch, "private": repo.Private}
}

func (g *GitHub) repository(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	repo, ok := g.scopedRepo(r, func(repo *Repo) bool { return repo.ID == id })
	if !ok {
		notFound(w)
		return
	}
	writeJSON(w, http.StatusOK, repoJSON(repo))
}

func (g *GitHub) branchRef(w http.ResponseWriter, r *http.Request) {
	fullName := r.PathValue("owner") + "/" + r.PathValue("repo")
	repo, ok := g.scopedRepo(r, func(repo *Repo) bool { return repo.FullName == fullName })
	if !ok {
		notFound(w)
		return
	}
	g.mu.Lock()
	sha, held := repo.Branches[r.PathValue("branch")]
	g.mu.Unlock()
	if !held {
		notFound(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ref": "refs/heads/" + r.PathValue("branch"), "object": map[string]any{"sha": sha, "type": "commit"}})
}

func (g *GitHub) commit(w http.ResponseWriter, r *http.Request) {
	fullName := r.PathValue("owner") + "/" + r.PathValue("repo")
	if _, ok := g.scopedRepo(r, func(repo *Repo) bool { return repo.FullName == fullName }); !ok {
		notFound(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sha": r.PathValue("sha"), "commit": map[string]any{"message": "commit " + r.PathValue("sha")}})
}

func (g *GitHub) exchangeCode(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || r.Form.Get("client_id") != ClientID || r.Form.Get("client_secret") != ClientSecret {
		writeJSON(w, http.StatusOK, map[string]string{"error": "incorrect_client_credentials"})
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	visible, ok := g.codes[r.Form.Get("code")]
	if !ok {
		writeJSON(w, http.StatusOK, map[string]string{"error": "bad_verification_code"})
		return
	}
	delete(g.codes, r.Form.Get("code"))
	token := "ghu_fake_" + r.Form.Get("code")
	g.userTokens[token] = visible
	writeJSON(w, http.StatusOK, map[string]string{"access_token": token, "token_type": "bearer"})
}

func (g *GitHub) userVisible(r *http.Request) (map[int64][]int64, bool) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), bearerPrefix)
	if !ok {
		return nil, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	visible, ok := g.userTokens[raw]
	return visible, ok
}

func (g *GitHub) userInstallations(w http.ResponseWriter, r *http.Request) {
	visible, ok := g.userVisible(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, badCredentials)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	listed := []map[string]any{}
	for id := range visible {
		listed = append(listed, map[string]any{"id": id, "app_id": AppID, "account": map[string]any{"login": g.installations[id].account}})
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(listed), "installations": listed})
}

func (g *GitHub) userRepositories(w http.ResponseWriter, r *http.Request) {
	visible, ok := g.userVisible(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, badCredentials)
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	g.mu.Lock()
	defer g.mu.Unlock()
	repoIDs, ok := visible[id]
	if !ok {
		notFound(w)
		return
	}
	listed := []map[string]any{}
	if r.URL.Query().Get("page") == "" || r.URL.Query().Get("page") == "1" {
		for _, repoID := range repoIDs {
			if repo, held := g.installations[id].repos[repoID]; held {
				listed = append(listed, repoJSON(repo))
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(repoIDs), "repositories": listed})
}

func (g *GitHub) revokeUserToken(w http.ResponseWriter, r *http.Request) {
	user, secret, ok := r.BasicAuth()
	if !ok || user != ClientID || secret != ClientSecret || r.PathValue("client") != ClientID {
		writeJSON(w, http.StatusUnauthorized, badCredentials)
		return
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	g.mu.Lock()
	g.revokedUsers = append(g.revokedUsers, body.AccessToken)
	delete(g.userTokens, body.AccessToken)
	g.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}
