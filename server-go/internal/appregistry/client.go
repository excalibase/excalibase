package appregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
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

// Untag removes one of the platform's push tags. Another tag on the same
// image keeps it; an image left with none is freed by zot's own GC.
func (c *Client) Untag(ctx context.Context, repository, tag string) error {
	if _, _, ok := SplitRepository(repository); !ok {
		return ErrInvalidName
	}
	if _, _, ok := ParsePushTag(tag); !ok {
		return fmt.Errorf("only the platform's push tags are removed, not %q", tag)
	}
	resp, err := c.do(ctx, http.MethodDelete, "/v2/"+repository+"/manifests/"+tag, Grant{Type: "repository", Name: repository, Actions: []string{"delete"}})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("untag %s:%s: registry answered %d", repository, tag, resp.StatusCode)
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

func (c *Client) do(ctx context.Context, method, target string, grant Grant) (*http.Response, error) {
	return c.send(ctx, method, target, grant, nil, 0, "")
}

// send makes one request with a token for grant alone; target is a path on the registry.
func (c *Client) send(ctx context.Context, method, target string, grant Grant, body io.Reader, size int64, contentType string) (*http.Response, error) {
	token, err := c.issuer.Mint("provisioning", []Grant{grant}, adminTokenTTL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+target, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.ContentLength = size
		req.Header.Set("Content-Type", contentType)
	}
	return c.http.Do(req)
}

// Blobs answers what an image holds: every blob and manifest under it, by
// digest and size, so a project's images can be counted with shared layers once.
func (c *Client) Blobs(ctx context.Context, repository, reference string) (map[string]int64, error) {
	if _, _, ok := SplitRepository(repository); !ok {
		return nil, ErrInvalidName
	}
	return c.blobsAt(ctx, Grant{Type: "repository", Name: repository, Actions: []string{"pull"}}, reference, true)
}

// blobsAt reads one manifest; an index's manifests are read once more, never deeper.
func (c *Client) blobsAt(ctx context.Context, grant Grant, reference string, top bool) (map[string]int64, error) {
	repository := grant.Name
	if _, _, tag := ParsePushTag(reference); !tag && !digestPattern.MatchString(reference) {
		return nil, fmt.Errorf("read %s: %q is not a push tag or a digest", repository, reference)
	}
	blobs := map[string]int64{}
	body, digest, err := c.manifest(ctx, repository, reference, grant)
	if err != nil {
		return nil, err
	}
	blobs[digest] = int64(len(body))
	var parsed struct {
		Manifests []ocispec.Descriptor `json:"manifests"`
		Config    ocispec.Descriptor   `json:"config"`
		Layers    []ocispec.Descriptor `json:"layers"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("read %s@%s: %w", repository, reference, err)
	}
	if len(parsed.Manifests) > 0 && !top {
		return nil, fmt.Errorf("read %s@%s: an index inside an index", repository, reference)
	}
	for _, child := range parsed.Manifests {
		nested, err := c.blobsAt(ctx, grant, string(child.Digest), false)
		if err != nil {
			return nil, err
		}
		for d, size := range nested {
			blobs[d] = size
		}
	}
	for _, blob := range append([]ocispec.Descriptor{parsed.Config}, parsed.Layers...) {
		if blob.Digest != "" {
			blobs[string(blob.Digest)] = blob.Size
		}
	}
	return blobs, nil
}

// manifestAccept is every manifest type an image the platform pushed may be.
const manifestAccept = ocispec.MediaTypeImageManifest + ", " + ocispec.MediaTypeImageIndex +
	", application/vnd.docker.distribution.manifest.v2+json, application/vnd.docker.distribution.manifest.list.v2+json"

func (c *Client) manifest(ctx context.Context, repository, reference string, grant Grant) ([]byte, string, error) {
	token, err := c.issuer.Mint("provisioning", []Grant{grant}, adminTokenTTL)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v2/"+repository+"/manifests/"+reference, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", manifestAccept)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("read %s@%s: registry answered %d", repository, reference, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxManifest+1))
	if err != nil || len(body) > maxManifest {
		return nil, "", fmt.Errorf("read %s@%s: a manifest over %d bytes", repository, reference, maxManifest)
	}
	sum := sha256.Sum256(body)
	return body, "sha256:" + hex.EncodeToString(sum[:]), nil
}
