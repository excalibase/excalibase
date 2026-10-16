// Package cfworkers is the small part of the Cloudflare Workers for Platforms
// API that Sites needs: upload a deploy's static assets into a user Worker of
// the platform's dispatch namespace, publish that Worker, and delete it.
package cfworkers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// DefaultBaseURL is the Cloudflare API origin; tests point Config.BaseURL at a fake.
const DefaultBaseURL = "https://api.cloudflare.com"

// maxResponseBytes bounds what is read of any API answer.
const maxResponseBytes = 1 << 20

// userAgent names this client; Cloudflare's edge refuses some library defaults.
const userAgent = "excalibase-sites/1.0"

// requestTimeout bounds one API call; an upload of a 25 MiB file fits well inside it.
const requestTimeout = 5 * time.Minute

// scriptNamePattern is what this client ever sends as a script name: no path
// separator or escape can reach the URL.
var scriptNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Config names the account and namespace user Workers live in. APIToken is a
// secret: it is sent only in the Authorization header and never formatted.
type Config struct {
	BaseURL   string
	AccountID string
	APIToken  string
	Namespace string
	HTTP      *http.Client
}

// Client calls the Workers API with the platform's token.
type Client struct {
	baseURL   string
	accountID string
	token     string
	namespace string
	http      *http.Client
}

// New refuses a config missing any setting rather than calling Cloudflare half-configured.
func New(cfg Config) (*Client, error) {
	if cfg.AccountID == "" || cfg.APIToken == "" || cfg.Namespace == "" {
		return nil, errors.New("cloudflare workers client needs an account id, an API token and a dispatch namespace")
	}
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	httpClient := cfg.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}
	return &Client{baseURL: baseURL, accountID: cfg.AccountID, token: cfg.APIToken, namespace: cfg.Namespace, http: httpClient}, nil
}

// APIError is a refusal Cloudflare answered; it carries Cloudflare's own message, never a credential.
type APIError struct {
	Status  int
	Codes   []int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("cloudflare answered %d: %s", e.Status, e.Message)
}

// envelope is the shape every v4 answer shares.
type envelope struct {
	Success bool            `json:"success"`
	Errors  []apiMessage    `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type apiMessage struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *Client) accountPath(parts ...string) string {
	escaped := make([]string, 0, len(parts)+3)
	escaped = append(escaped, "client", "v4", "accounts", url.PathEscape(c.accountID))
	for _, part := range parts {
		escaped = append(escaped, url.PathEscape(part))
	}
	return c.baseURL + "/" + strings.Join(escaped, "/")
}

func (c *Client) scriptPath(script string, suffix ...string) (string, error) {
	if !scriptNamePattern.MatchString(script) {
		return "", fmt.Errorf("invalid worker script name %q", script)
	}
	parts := append([]string{"workers", "dispatch", "namespaces", c.namespace, "scripts", script}, suffix...)
	return c.accountPath(parts...), nil
}

// call sends one request with bearer and decodes the envelope; a non-2xx
// answer is an APIError. The response status is returned for callers that
// tell 201 from 202.
func (c *Client) call(ctx context.Context, method, target, bearer, contentType string, body io.Reader) (int, json.RawMessage, error) {
	request, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return 0, nil, fmt.Errorf("build cloudflare request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set("User-Agent", userAgent)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("call cloudflare %s %s: %w", method, redactedPath(target), unwrapURLError(err))
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return response.StatusCode, nil, fmt.Errorf("read cloudflare answer: %w", err)
	}
	var decoded envelope
	decodeErr := json.Unmarshal(raw, &decoded)
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return response.StatusCode, nil, apiError(response.StatusCode, decoded.Errors)
	}
	if decodeErr != nil {
		return response.StatusCode, nil, fmt.Errorf("decode cloudflare answer: %w", decodeErr)
	}
	return response.StatusCode, decoded.Result, nil
}

func apiError(status int, messages []apiMessage) *APIError {
	apiErr := &APIError{Status: status, Message: http.StatusText(status)}
	texts := make([]string, 0, len(messages))
	for _, message := range messages {
		apiErr.Codes = append(apiErr.Codes, message.Code)
		texts = append(texts, message.Message)
	}
	if len(texts) > 0 {
		apiErr.Message = strings.Join(texts, "; ")
	}
	return apiErr
}

// redactedPath keeps the path of a target for an error without its query.
func redactedPath(target string) string {
	parsed, err := url.Parse(target)
	if err != nil {
		return "(unparseable url)"
	}
	return parsed.Path
}

// unwrapURLError drops the *url.Error wrapper, which would repeat the full URL.
func unwrapURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}
