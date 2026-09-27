package sdkkeys

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// ErrAuthUnavailable means the auth service could not be reached or could
// not answer; the caller may retry.
var ErrAuthUnavailable = errors.New("the auth service is unavailable")

// RefusedError is a refusal the auth service gave for the request itself.
type RefusedError struct {
	Status  int
	Message string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("auth refused the request (%d): %s", e.Status, e.Message)
}

// Key is a stored api key as auth lists it: never the key itself.
type Key struct {
	ID         int64   `json:"id"`
	KeyPrefix  string  `json:"keyPrefix"`
	KeyType    string  `json:"keyType"`
	Name       string  `json:"name"`
	CreatedAt  string  `json:"createdAt"`
	LastUsedAt *string `json:"lastUsedAt,omitempty"`
}

// CreatedKey carries the full key, which auth returns exactly once.
type CreatedKey struct {
	Key
	Plaintext string `json:"plaintext"`
}

type CreateRequest struct {
	Name    string `json:"name"`
	KeyType string `json:"keyType"`
}

type tokenSigner interface {
	Sign(projectID, orgSlug string) (string, error)
}

// Client calls excalibase-auth's api-key routes for a project.
type Client struct {
	baseURL string
	signer  tokenSigner
	http    *http.Client
}

func NewClient(baseURL string, signer tokenSigner, httpClient *http.Client) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), signer: signer, http: httpClient}
}

var pathSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func (c *Client) List(ctx context.Context, orgSlug, projectID string) ([]Key, error) {
	var listed struct {
		Keys []Key `json:"keys"`
	}
	if err := c.do(ctx, http.MethodGet, orgSlug, projectID, "", nil, &listed); err != nil {
		return nil, err
	}
	if listed.Keys == nil {
		listed.Keys = []Key{}
	}
	return listed.Keys, nil
}

func (c *Client) Create(ctx context.Context, orgSlug, projectID string, req CreateRequest) (*CreatedKey, error) {
	var created CreatedKey
	if err := c.do(ctx, http.MethodPost, orgSlug, projectID, "", req, &created); err != nil {
		return nil, err
	}
	return &created, nil
}

func (c *Client) Revoke(ctx context.Context, orgSlug, projectID string, keyID int64) error {
	return c.do(ctx, http.MethodDelete, orgSlug, projectID, strconv.FormatInt(keyID, 10), nil, nil)
}

func (c *Client) do(ctx context.Context, method, orgSlug, projectID, keyID string, body, out any) error {
	if !pathSegment.MatchString(orgSlug) || !pathSegment.MatchString(projectID) {
		return errors.New("unsafe org or project identifier")
	}
	req, err := c.newRequest(ctx, method, orgSlug, projectID, keyID, body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAuthUnavailable, err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= http.StatusInternalServerError {
		return fmt.Errorf("%w: status %d", ErrAuthUnavailable, resp.StatusCode)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return &RefusedError{Status: resp.StatusCode, Message: errorMessage(payload)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("decode auth response: %w", err)
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, orgSlug, projectID, keyID string, body any) (*http.Request, error) {
	token, err := c.signer.Sign(projectID, orgSlug)
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	url := fmt.Sprintf("%s/auth/%s/%s/api-keys/%s", c.baseURL, orgSlug, projectID, keyID)
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func errorMessage(payload []byte) string {
	var decoded struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(payload, &decoded) == nil && decoded.Error != "" {
		return decoded.Error
	}
	return "request refused"
}
