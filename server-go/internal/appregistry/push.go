package appregistry

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Push uploads a layout to repository: blobs the registry lacks, then the
// manifests an index names, then the root under its push tag. It answers the
// root's digest, the one the app deploys by.
func (c *Client) Push(ctx context.Context, repository string, layout *Layout, at time.Time) (string, error) {
	if _, _, ok := SplitRepository(repository); !ok {
		return "", ErrInvalidName
	}
	tag, err := PushTag(string(layout.Root.Digest), at)
	if err != nil {
		return "", err
	}
	grant := Grant{Type: "repository", Name: repository, Actions: []string{"pull", "push"}}
	for _, blob := range layout.Blobs {
		if err := c.pushBlob(ctx, repository, grant, layout, blob); err != nil {
			return "", err
		}
	}
	for _, child := range layout.Children {
		if err := c.putManifest(ctx, repository, grant, layout, child, string(child.Digest)); err != nil {
			return "", err
		}
	}
	if err := c.putManifest(ctx, repository, grant, layout, layout.Root, tag); err != nil {
		return "", err
	}
	return string(layout.Root.Digest), nil
}

func (c *Client) pushBlob(ctx context.Context, repository string, grant Grant, layout *Layout, blob ocispec.Descriptor) error {
	head, err := c.do(ctx, http.MethodHead, "/v2/"+repository+"/blobs/"+string(blob.Digest), grant)
	if err != nil {
		return err
	}
	head.Body.Close()
	if head.StatusCode == http.StatusOK {
		return nil
	}
	start, err := c.do(ctx, http.MethodPost, "/v2/"+repository+"/blobs/uploads/", grant)
	if err != nil {
		return err
	}
	start.Body.Close()
	if start.StatusCode != http.StatusAccepted {
		return fmt.Errorf("start the upload of %s: registry answered %d", blob.Digest, start.StatusCode)
	}
	target, err := c.uploadTarget(start.Header.Get("Location"), string(blob.Digest))
	if err != nil {
		return err
	}
	return c.putFile(ctx, target, grant, layout, blob, "application/octet-stream")
}

// uploadTarget keeps the registry's upload location on the registry itself.
func (c *Client) uploadTarget(location, digest string) (string, error) {
	parsed, err := url.Parse(location)
	if err != nil || location == "" {
		return "", fmt.Errorf("the registry gave no upload location")
	}
	if parsed.IsAbs() && parsed.Scheme+"://"+parsed.Host != c.base {
		return "", fmt.Errorf("the registry sent the upload to %s", parsed.Host)
	}
	query := parsed.Query()
	query.Set("digest", digest)
	return parsed.EscapedPath() + "?" + query.Encode(), nil
}

func (c *Client) putManifest(ctx context.Context, repository string, grant Grant, layout *Layout, desc ocispec.Descriptor, reference string) error {
	return c.putFile(ctx, "/v2/"+repository+"/manifests/"+reference, grant, layout, desc, desc.MediaType)
}

// putFile sends a verified blob from the layout as the body of one PUT.
func (c *Client) putFile(ctx context.Context, target string, grant Grant, layout *Layout, desc ocispec.Descriptor, contentType string) error {
	file, err := layout.Open(desc)
	if err != nil {
		return err
	}
	defer file.Close()
	resp, err := c.send(ctx, http.MethodPut, target, grant, readerOnly{file}, desc.Size, contentType)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxListBody))
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("push %s: registry answered %d", desc.Digest, resp.StatusCode)
	}
	if got := resp.Header.Get("Docker-Content-Digest"); got != "" && got != string(desc.Digest) {
		return fmt.Errorf("push %s: the registry stored %s", desc.Digest, got)
	}
	return nil
}

// readerOnly hides the file's other methods, so the transport streams it as sized.
type readerOnly struct{ file *os.File }

func (r readerOnly) Read(p []byte) (int, error) { return r.file.Read(p) }
