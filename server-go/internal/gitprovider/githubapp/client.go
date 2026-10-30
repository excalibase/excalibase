// Package githubapp is GitHub as a git host for site builds (EXC-354): a
// GitHub App the owner registers, read-only on repository contents, with
// push webhooks. Who may connect what is checked through the user's own
// GitHub sign-in; builds clone with installation tokens scoped to one repo.
package githubapp

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/excalibase/provisioning-poc/internal/gitprovider"
)

const (
	appJWTLifetime = 9 * time.Minute
	// clockSkew backdates the App JWT, as GitHub recommends.
	clockSkew       = time.Minute
	requestTimeout  = 20 * time.Second
	maxResponseBody = 4 << 20
	// maxInstallations and maxRepoPages bound what one sign-in lists.
	maxInstallations = 100
	maxRepoPages     = 10
	pageSize         = 100
)

// Config is the App the owner registered, and where GitHub answers.
type Config struct {
	AppID         int64
	AppSlug       string
	ClientID      string
	ClientSecret  string
	PrivateKeyPEM string
	WebhookSecret string
	APIURL        string
	WebURL        string
}

// Client talks to GitHub as the App.
type Client struct {
	config Config
	key    *rsa.PrivateKey
	http   *http.Client
	now    func() time.Time
}

var (
	_ gitprovider.Provider    = (*Client)(nil)
	_ gitprovider.FileChecker = (*Client)(nil)
)

func New(config Config) (*Client, error) {
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(config.PrivateKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("the GitHub App private key does not parse: %w", err)
	}
	config.APIURL = strings.TrimRight(config.APIURL, "/")
	config.WebURL = strings.TrimRight(config.WebURL, "/")
	return &Client{config: config, key: key, http: &http.Client{Timeout: requestTimeout}, now: time.Now}, nil
}

func (c *Client) Name() string { return "github" }

func (c *Client) AuthorizeURL(state, redirectURL string) string {
	query := url.Values{"client_id": {c.config.ClientID}, "redirect_uri": {redirectURL}, "state": {state}}
	return c.config.WebURL + "/login/oauth/authorize?" + query.Encode()
}

func (c *Client) InstallURL(state string) string {
	return c.config.WebURL + "/apps/" + url.PathEscape(c.config.AppSlug) + "/installations/new?" + url.Values{"state": {state}}.Encode()
}

func (c *Client) CloneURL(repository gitprovider.Repository) string {
	return c.config.WebURL + "/" + repository.FullName + ".git"
}

// appJWT authenticates as the App itself, for minting installation tokens.
func (c *Client) appJWT() (string, error) {
	now := c.now()
	claims := jwt.RegisteredClaims{
		Issuer:    strconv.FormatInt(c.config.AppID, 10),
		IssuedAt:  jwt.NewNumericDate(now.Add(-clockSkew)),
		ExpiresAt: jwt.NewNumericDate(now.Add(appJWTLifetime)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(c.key)
}

// APIError is GitHub refusing a call; Status is its HTTP status.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("GitHub answered %d: %s", e.Status, e.Message)
}

func (e *APIError) Is(target error) bool {
	return target == gitprovider.ErrNotFound && e.Status == http.StatusNotFound
}

type call struct {
	method      string
	url         string
	bearer      string
	body        any
	form        url.Values
	basicUser   string
	basicSecret string
}

func (c *Client) do(ctx context.Context, request call, out any) error {
	var body io.Reader
	contentType := ""
	switch {
	case request.form != nil:
		body, contentType = strings.NewReader(request.form.Encode()), "application/x-www-form-urlencoded"
	case request.body != nil:
		encoded, err := json.Marshal(request.body)
		if err != nil {
			return fmt.Errorf("encode GitHub request: %w", err)
		}
		body, contentType = bytes.NewReader(encoded), "application/json"
	}
	httpRequest, err := http.NewRequestWithContext(ctx, request.method, request.url, body)
	if err != nil {
		return fmt.Errorf("build GitHub request: %w", err)
	}
	httpRequest.Header.Set("Accept", "application/vnd.github+json")
	httpRequest.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if contentType != "" {
		httpRequest.Header.Set("Content-Type", contentType)
	}
	if request.bearer != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+request.bearer)
	}
	if request.basicUser != "" {
		httpRequest.SetBasicAuth(request.basicUser, request.basicSecret)
	}
	response, err := c.http.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("reach GitHub: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	if err != nil {
		return fmt.Errorf("read GitHub's answer: %w", err)
	}
	if response.StatusCode >= 300 {
		var failure struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &failure)
		return &APIError{Status: response.StatusCode, Message: failure.Message}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode GitHub's answer: %w", err)
	}
	return nil
}

// installationToken mints a token for one repository with only the permissions asked for.
func (c *Client) installationToken(ctx context.Context, installationID, repositoryID int64, permissions map[string]string) (gitprovider.CloneCredential, error) {
	signed, err := c.appJWT()
	if err != nil {
		return gitprovider.CloneCredential{}, fmt.Errorf("sign the App token: %w", err)
	}
	var minted struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	err = c.do(ctx, call{
		method: http.MethodPost, url: fmt.Sprintf("%s/app/installations/%d/access_tokens", c.config.APIURL, installationID), bearer: signed,
		body: map[string]any{"repository_ids": []int64{repositoryID}, "permissions": permissions},
	}, &minted)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusUnprocessableEntity {
			return gitprovider.CloneCredential{}, fmt.Errorf("%w: the installation no longer covers the repository", gitprovider.ErrNotFound)
		}
		return gitprovider.CloneCredential{}, err
	}
	if minted.Token == "" {
		return gitprovider.CloneCredential{}, errors.New("GitHub minted an empty installation token")
	}
	return gitprovider.CloneCredential{Token: minted.Token, ExpiresAt: minted.ExpiresAt}, nil
}

var contentsRead = map[string]string{"contents": "read"}

func (c *Client) CloneCredential(ctx context.Context, installationID, repositoryID int64) (gitprovider.CloneCredential, error) {
	return c.installationToken(ctx, installationID, repositoryID, contentsRead)
}

func (c *Client) RevokeCloneCredential(ctx context.Context, token string) error {
	return c.do(ctx, call{method: http.MethodDelete, url: c.config.APIURL + "/installation/token", bearer: token}, nil)
}

type repositoryJSON struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}

func (r repositoryJSON) repository() gitprovider.Repository {
	return gitprovider.Repository{ID: r.ID, FullName: r.FullName, DefaultBranch: r.DefaultBranch, Private: r.Private}
}

// Repository reads a repository by id, so a rename still finds it.
func (c *Client) Repository(ctx context.Context, installationID, repositoryID int64) (gitprovider.Repository, error) {
	credential, err := c.installationToken(ctx, installationID, repositoryID, contentsRead)
	if err != nil {
		return gitprovider.Repository{}, err
	}
	defer c.revokeQuietly(ctx, credential.Token)
	var found repositoryJSON
	if err := c.do(ctx, call{method: http.MethodGet, url: fmt.Sprintf("%s/repositories/%d", c.config.APIURL, repositoryID), bearer: credential.Token}, &found); err != nil {
		return gitprovider.Repository{}, err
	}
	return found.repository(), nil
}

func (c *Client) BranchHead(ctx context.Context, installationID int64, repository gitprovider.Repository, branch string) (gitprovider.Commit, error) {
	credential, err := c.installationToken(ctx, installationID, repository.ID, contentsRead)
	if err != nil {
		return gitprovider.Commit{}, err
	}
	defer c.revokeQuietly(ctx, credential.Token)
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	refURL := fmt.Sprintf("%s/repos/%s/git/ref/heads/%s", c.config.APIURL, repository.FullName, escapeRef(branch))
	if err := c.do(ctx, call{method: http.MethodGet, url: refURL, bearer: credential.Token}, &ref); err != nil {
		return gitprovider.Commit{}, err
	}
	commit := gitprovider.Commit{SHA: ref.Object.SHA}
	var detail struct {
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	}
	commitURL := fmt.Sprintf("%s/repos/%s/commits/%s", c.config.APIURL, repository.FullName, url.PathEscape(commit.SHA))
	if err := c.do(ctx, call{method: http.MethodGet, url: commitURL, bearer: credential.Token}, &detail); err != nil {
		return gitprovider.Commit{}, err
	}
	commit.Message = detail.Commit.Message
	return commit, nil
}

// FileExists reads the path's contents entry at the commit; a directory or a missing path is no file.
func (c *Client) FileExists(ctx context.Context, installationID int64, repository gitprovider.Repository, commit, path string) (bool, error) {
	credential, err := c.installationToken(ctx, installationID, repository.ID, contentsRead)
	if err != nil {
		return false, err
	}
	defer c.revokeQuietly(ctx, credential.Token)
	var entry json.RawMessage
	contentsURL := fmt.Sprintf("%s/repos/%s/contents/%s?ref=%s", c.config.APIURL, repository.FullName, escapeRef(path), url.QueryEscape(commit))
	err = c.do(ctx, call{method: http.MethodGet, url: contentsURL, bearer: credential.Token}, &entry)
	if errors.Is(err, gitprovider.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var file struct {
		Type string `json:"type"`
	}
	// A directory answers a JSON array, which does not decode into an object.
	if json.Unmarshal(entry, &file) != nil {
		return false, nil
	}
	return file.Type == "file", nil
}

// escapeRef escapes each segment of a branch name, keeping its slashes.
func escapeRef(branch string) string {
	segments := strings.Split(branch, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

// revokeQuietly ends a token minted for one read; it expires within the hour anyway.
func (c *Client) revokeQuietly(ctx context.Context, token string) {
	if err := c.RevokeCloneCredential(context.WithoutCancel(ctx), token); err != nil {
		log.Printf("github: revoke a read-only installation token: %v", err)
	}
}
