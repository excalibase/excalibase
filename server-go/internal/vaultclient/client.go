package vaultclient

import (
	"bytes"
	"context"
	"errors"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	errInvalidPath    = "invalid vault path: %w"
	errCreateRequest  = "create request: %w"
	errVaultRequest   = "vault request: %w"
	errVaultSealed    = "vault is sealed"
	errVaultStatusFmt = "vault returned %d"
	errDecodeResponse = "decode response: %w"
)


// VaultClient defines the interface for vault operations.
// Both *vault.Vault (in-process) and *HTTPClient (remote) satisfy this.
type VaultClient interface {
	Get(path string) (map[string]string, error)
	Put(path string, data map[string]string) error
	Delete(path string) error
	// DeletePrefix removes every secret under prefix in one server round-trip.
	// Returns the number of paths deleted. Empty prefix must be rejected by
	// implementations to avoid wiping the vault.
	DeletePrefix(prefix string) (int, error)
	List(prefix string) ([]string, error)
	Sealed() bool
	GetPublicKey() (string, error)
}

// HTTPClient talks to the vault service over HTTP.
type HTTPClient struct {
	baseURL    string
	pat        string
	httpClient *http.Client
}

// defaultRequestTimeout is the per-request fallback when the caller's
// context has no deadline. The interface predates context plumbing; once
// every call site supplies a real ctx, this can be removed.
const defaultRequestTimeout = 10 * time.Second

func NewHTTPClient(baseURL, pat string) *HTTPClient {
	return &HTTPClient{
		baseURL: baseURL,
		pat:     pat,
		httpClient: &http.Client{
			Timeout: defaultRequestTimeout,
		},
	}
}

// secretsURL builds /secrets/<escaped>/<escaped>... so user-supplied
// path segments cannot inject `?`, `#`, or `%2F` into the URL. Each `/`
// in the input is treated as a real path separator; everything else is
// PathEscape'd. Refuses empty segments and `..` traversal.
func (c *HTTPClient) secretsURL(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	segments := strings.Split(path, "/")
	parts := make([]string, 0, len(segments))
	for _, s := range segments {
		if s == "" || s == "." || s == ".." {
			return "", fmt.Errorf("invalid path segment: %q", s)
		}
		parts = append(parts, url.PathEscape(s))
	}
	return c.baseURL + "/secrets/" + strings.Join(parts, "/"), nil
}

// listURL builds /secrets-list?prefix=... with proper query escaping.
func (c *HTTPClient) listURL(prefix string) string {
	u := c.baseURL + "/secrets-list"
	if prefix != "" {
		q := url.Values{}
		q.Set("prefix", prefix)
		u += "?" + q.Encode()
	}
	return u
}

// requestCtx returns a context with a default timeout if the caller's
// context has none. Honours caller cancellation either way.
func requestCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), defaultRequestTimeout)
}

func (c *HTTPClient) Get(path string) (map[string]string, error) {
	u, err := c.secretsURL(path)
	if err != nil {
		return nil, fmt.Errorf(errInvalidPath, err)
	}
	ctx, cancel := requestCtx()
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf(errCreateRequest, err)
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf(errVaultRequest, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("secret not found: %s", path)
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		return nil, errors.New(errVaultSealed)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(errVaultStatusFmt, resp.StatusCode)
	}

	var data map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf(errDecodeResponse, err)
	}
	return data, nil
}

func (c *HTTPClient) Put(path string, data map[string]string) error {
	u, err := c.secretsURL(path)
	if err != nil {
		return fmt.Errorf(errInvalidPath, err)
	}
	body, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal data: %w", err)
	}

	ctx, cancel := requestCtx()
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "PUT", u, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf(errCreateRequest, err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf(errVaultRequest, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusServiceUnavailable {
		return errors.New(errVaultSealed)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf(errVaultStatusFmt, resp.StatusCode)
	}
	return nil
}

func (c *HTTPClient) Delete(path string) error {
	u, err := c.secretsURL(path)
	if err != nil {
		return fmt.Errorf(errInvalidPath, err)
	}
	ctx, cancel := requestCtx()
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "DELETE", u, nil)
	if err != nil {
		return fmt.Errorf(errCreateRequest, err)
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf(errVaultRequest, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusServiceUnavailable {
		return errors.New(errVaultSealed)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf(errVaultStatusFmt, resp.StatusCode)
	}
	return nil
}

func (c *HTTPClient) DeletePrefix(prefix string) (int, error) {
	if prefix == "" {
		return 0, fmt.Errorf("DeletePrefix: empty prefix not allowed")
	}
	ctx, cancel := requestCtx()
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "DELETE", c.listURL(prefix), nil)
	if err != nil {
		return 0, fmt.Errorf(errCreateRequest, err)
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf(errVaultRequest, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusServiceUnavailable {
		return 0, errors.New(errVaultSealed)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf(errVaultStatusFmt, resp.StatusCode)
	}
	var result struct {
		Deleted int `json:"deleted"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf(errDecodeResponse, err)
	}
	return result.Deleted, nil
}

func (c *HTTPClient) List(prefix string) ([]string, error) {
	ctx, cancel := requestCtx()
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", c.listURL(prefix), nil)
	if err != nil {
		return nil, fmt.Errorf(errCreateRequest, err)
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf(errVaultRequest, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusServiceUnavailable {
		return nil, errors.New(errVaultSealed)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(errVaultStatusFmt, resp.StatusCode)
	}

	var result struct {
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf(errDecodeResponse, err)
	}
	return result.Paths, nil
}

func (c *HTTPClient) Sealed() bool {
	ctx, cancel := requestCtx()
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/status", nil)
	if err != nil {
		return true
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return true
	}
	defer resp.Body.Close()

	var status struct {
		Sealed bool `json:"sealed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return true
	}
	return status.Sealed
}

func (c *HTTPClient) GetPublicKey() (string, error) {
	ctx, cancel := requestCtx()
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/pki/public-key", nil)
	if err != nil {
		return "", fmt.Errorf(errCreateRequest, err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf(errVaultRequest, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusServiceUnavailable {
		return "", errors.New(errVaultSealed)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf(errVaultStatusFmt, resp.StatusCode)
	}

	var data map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", fmt.Errorf(errDecodeResponse, err)
	}
	return data["key"], nil
}

func (c *HTTPClient) setAuth(req *http.Request) {
	if c.pat != "" {
		req.Header.Set("Authorization", "Bearer "+c.pat)
	}
}
