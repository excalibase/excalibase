package appregistry

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

// ErrInternalImage refuses an image a user names on the platform's own
// registry: only the platform's builds deploy from it (EXC-540).
var ErrInternalImage = errors.New("images on the platform's own registry come from its git builds; name your own registry's image instead")

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Internal is the platform's registry as provisioning sees it: the host
// images name, and the issuer of its tokens and pull credentials.
type Internal struct {
	host   string
	issuer *Issuer
}

func NewInternal(host string, issuer *Issuer) (*Internal, error) {
	normalized, err := apphost.NormalizeRegistry(host)
	if err != nil {
		return nil, fmt.Errorf("the internal registry host: %w", err)
	}
	if normalized == "docker.io" {
		return nil, errors.New("the internal registry host cannot be Docker Hub")
	}
	if issuer == nil {
		return nil, errors.New("the internal registry needs its token issuer")
	}
	return &Internal{host: normalized, issuer: issuer}, nil
}

// Host is the registry host images name; nil has none.
func (in *Internal) Host() string {
	if in == nil {
		return ""
	}
	return in.host
}

// Names answers whether image is on the internal registry, however its host is spelled.
func (in *Internal) Names(image string) bool {
	if in == nil {
		return false
	}
	host, err := apphost.NormalizeRegistry(apphost.ImageRegistry(image))
	return err == nil && host == in.host
}

// RefuseUserImage is for every image a person or a token names: the deploy API, Studio, MCP.
func (in *Internal) RefuseUserImage(image string) error {
	if in.Names(image) {
		return ErrInternalImage
	}
	return nil
}

// Image is the reference a build deploys: always by digest.
func (in *Internal) Image(projectID, appName, digest string) (string, error) {
	repository, err := Repository(projectID, appName)
	if err != nil {
		return "", err
	}
	if !digestPattern.MatchString(digest) {
		return "", fmt.Errorf("an internal image is deployed by its sha256 digest, not %q", digest)
	}
	return in.host + "/" + repository + "@" + digest, nil
}

// PullCredential is what the project's pull secret carries.
func (in *Internal) PullCredential(projectID string) PullCredential {
	return in.issuer.PullCredential(projectID)
}

// Issuer mints the platform's own push and delete tokens.
func (in *Internal) Issuer() *Issuer { return in.issuer }
