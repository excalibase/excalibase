// Package endusers lets Studio list a project's end users and set their role
// through excalibase-auth, which alone stores them. Each call carries a
// one-minute user-admin token naming the platform user who asked; the
// Studio session itself never leaves the control plane.
package endusers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// ErrAuthUnavailable means the auth service could not be reached or failed
// to answer; the caller may retry.
var ErrAuthUnavailable = errors.New("the auth service is unavailable")

// RefusedError is a 4xx the auth service gave for the request itself.
type RefusedError struct {
	Status  int
	Message string
	Code    string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("auth refused the request (%d): %s", e.Status, e.Message)
}

// RoleChange is the body of a role change. Auth defaults AllowedRoles to
// [Role] when it is omitted.
type RoleChange struct {
	Role         string   `json:"role"`
	AllowedRoles []string `json:"allowedRoles,omitempty"`
}

// EndUser is the account auth answers a role change with.
type EndUser struct {
	ID           int64    `json:"id"`
	Email        string   `json:"email"`
	Role         string   `json:"role"`
	AllowedRoles []string `json:"allowedRoles"`
}

// Page is optional paging; a nil field is not sent.
type Page struct {
	Limit  *int
	Offset *int
}

type userAdminSigner interface {
	SignUserAdmin(projectID, orgSlug, actor string) (string, error)
}

// Client calls excalibase-auth's end-user routes for a project.
type Client struct {
	baseURL string
	signer  userAdminSigner
	http    *http.Client
}

func NewClient(baseURL string, signer userAdminSigner, httpClient *http.Client) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), signer: signer, http: httpClient}
}

var pathSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// List relays auth's page of end users as auth wrote it.
func (c *Client) List(ctx context.Context, orgSlug, projectID, actor string, page Page) (json.RawMessage, error) {
	query := url.Values{}
	if page.Limit != nil {
		query.Set("limit", strconv.Itoa(*page.Limit))
	}
	if page.Offset != nil {
		query.Set("offset", strconv.Itoa(*page.Offset))
	}
	payload, err := c.do(ctx, call{method: http.MethodGet, orgSlug: orgSlug, projectID: projectID, actor: actor, path: "users", query: query})
	if err != nil {
		return nil, err
	}
	if !json.Valid(payload) {
		return nil, errors.New("decode auth response: not JSON")
	}
	return json.RawMessage(payload), nil
}

// SetRole sets userID's role and the roles the account may switch to.
func (c *Client) SetRole(ctx context.Context, orgSlug, projectID, actor string, userID int64, change RoleChange) (*EndUser, error) {
	payload, err := c.do(ctx, call{
		method: http.MethodPut, orgSlug: orgSlug, projectID: projectID, actor: actor,
		path: "users/" + strconv.FormatInt(userID, 10) + "/role", body: change,
	})
	if err != nil {
		return nil, err
	}
	var user EndUser
	if err := json.Unmarshal(payload, &user); err != nil {
		return nil, fmt.Errorf("decode auth response: %w", err)
	}
	return &user, nil
}

type call struct {
	method, orgSlug, projectID, actor, path string
	query                                   url.Values
	body                                    any
}

func (c *Client) do(ctx context.Context, spec call) ([]byte, error) {
	if !pathSegment.MatchString(spec.orgSlug) || !pathSegment.MatchString(spec.projectID) {
		return nil, errors.New("unsafe org or project identifier")
	}
	req, err := c.newRequest(ctx, spec)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAuthUnavailable, err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= http.StatusInternalServerError {
		return nil, fmt.Errorf("%w: status %d", ErrAuthUnavailable, resp.StatusCode)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, refusal(resp.StatusCode, payload)
	}
	return payload, nil
}

// newRequest builds the call from scratch: only the minted token and a JSON
// content type are sent, never a header of the Studio request.
func (c *Client) newRequest(ctx context.Context, spec call) (*http.Request, error) {
	token, err := c.signer.SignUserAdmin(spec.projectID, spec.orgSlug, spec.actor)
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if spec.body != nil {
		encoded, err := json.Marshal(spec.body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	target := fmt.Sprintf("%s/auth/%s/%s/%s", c.baseURL, spec.orgSlug, spec.projectID, spec.path)
	if len(spec.query) > 0 {
		target += "?" + spec.query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, spec.method, target, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func refusal(status int, payload []byte) *RefusedError {
	var decoded struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	_ = json.Unmarshal(payload, &decoded)
	message := decoded.Error
	if message == "" {
		message = "request refused"
	}
	return &RefusedError{Status: status, Message: message, Code: decoded.Code}
}
