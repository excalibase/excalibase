package appregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	// adminTokenTTL covers one request; each call mints its own.
	adminTokenTTL = 5 * time.Minute
	// maxListBody caps one page of the catalog or a tag list.
	maxListBody = 1 << 20
	// maxPages bounds a listing a registry keeps paging.
	maxPages = 10_000
)

// Client is provisioning's own access to the registry: the catalog, tag lists
// and deletes, each with a token it mints for that one request.
type Client struct {
	base     string
	issuer   *Issuer
	http     *http.Client
	pageSize int
}

// NewClient talks to the registry at baseURL, the in-cluster Service.
func NewClient(baseURL string, issuer *Issuer) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" {
		return nil, fmt.Errorf("APP_REGISTRY_URL must be http(s)://host[:port], got %q", baseURL)
	}
	if issuer == nil {
		return nil, errors.New("the registry client needs the token issuer")
	}
	return &Client{
		base:   parsed.Scheme + "://" + parsed.Host,
		issuer: issuer,
		// A redirect would carry the token elsewhere; the registry never sends one.
		http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		pageSize: 1000,
	}, nil
}

// Repositories lists every repository; the catalog grant is the legacy
// repository::pull zot reads as the catalog only.
func (c *Client) Repositories(ctx context.Context) ([]string, error) {
	return c.list(ctx, "/v2/_catalog", Grant{Type: "repository", Name: "", Actions: []string{"pull"}}, "repositories")
}

// Tags lists a repository's tags; one that holds nothing has none.
func (c *Client) Tags(ctx context.Context, repository string) ([]string, error) {
	if _, _, ok := SplitRepository(repository); !ok {
		return nil, ErrInvalidName
	}
	return c.list(ctx, "/v2/"+repository+"/tags/list", Grant{Type: "repository", Name: repository, Actions: []string{"pull"}}, "tags")
}

// Delete removes a manifest and every tag on it; zot's own GC then frees the blobs.
func (c *Client) Delete(ctx context.Context, repository, digest string) error {
	if _, _, ok := SplitRepository(repository); !ok {
		return ErrInvalidName
	}
	if !digestPattern.MatchString(digest) {
		return fmt.Errorf("a delete names a sha256 digest, not %q", digest)
	}
	resp, err := c.do(ctx, http.MethodDelete, "/v2/"+repository+"/manifests/"+digest, Grant{Type: "repository", Name: repository, Actions: []string{"delete"}})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("delete %s@%s: registry answered %d", repository, digest, resp.StatusCode)
}

// list reads a paged listing (n and last, as the distribution spec pages).
func (c *Client) list(ctx context.Context, path string, grant Grant, key string) ([]string, error) {
	var all []string
	last := ""
	for range maxPages {
		query := url.Values{"n": {strconv.Itoa(c.pageSize)}}
		if last != "" {
			query.Set("last", last)
		}
		items, found, err := c.page(ctx, path+"?"+query.Encode(), grant, key)
		if err != nil || !found {
			return all, err
		}
		all = append(all, items...)
		if len(items) < c.pageSize {
			return all, nil
		}
		last = items[len(items)-1]
	}
	return nil, fmt.Errorf("%s: more than %d pages", path, maxPages)
}

// page reads one page; found is false for a repository the registry does not know.
func (c *Client) page(ctx context.Context, target string, grant Grant, key string) ([]string, bool, error) {
	resp, err := c.do(ctx, http.MethodGet, target, grant)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound && key == "tags" {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("list %s: registry answered %d", grant.Name, resp.StatusCode)
	}
	// The catalog answers {"repositories": [...]}, a tag list {"name": ..., "tags": [...]}.
	var body struct {
		Repositories []string `json:"repositories"`
		Tags         []string `json:"tags"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxListBody)).Decode(&body); err != nil {
		return nil, false, fmt.Errorf("list %s: %w", grant.Name, err)
	}
	if key == "tags" {
		return body.Tags, true, nil
	}
	return body.Repositories, true, nil
}

func (c *Client) do(ctx context.Context, method, path string, grant Grant) (*http.Response, error) {
	token, err := c.issuer.Mint("provisioning", []Grant{grant}, adminTokenTTL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return c.http.Do(req)
}
