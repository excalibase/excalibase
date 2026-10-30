package appregistry

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

// ErrInternalImage refuses an image a user names on the platform's own
// registry: only the platform's builds deploy from it (EXC-540).
var ErrInternalImage = errors.New("images on the platform's own registry come from its git builds; name your own registry's image instead")

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Internal is the platform's registry as provisioning sees it: the host
// images name, the other names that reach the same registry, and the issuer
// of its tokens and pull credentials.
type Internal struct {
	host        string
	names       map[string]bool
	issuer      *Issuer
	generations PullGenerations
}

// PullGenerations is each project's pull-password generation.
type PullGenerations interface {
	PullGeneration(projectID string) (int, error)
}

// NewInternal takes the host images name and its aliases (the Service's DNS
// names, its ClusterIP): an image naming any of them, on any port, is ours.
// Alone it only guards; Issuing adds the tokens and pull credentials.
func NewInternal(host string, aliases []string) (*Internal, error) {
	normalized, err := apphost.NormalizeRegistry(host)
	if err != nil {
		return nil, fmt.Errorf("the internal registry host: %w", err)
	}
	if normalized == "docker.io" {
		return nil, errors.New("the internal registry host cannot be Docker Hub")
	}
	names := map[string]bool{hostname(normalized): true}
	for _, alias := range aliases {
		if alias = strings.TrimSpace(alias); alias != "" {
			names[hostname(alias)] = true
		}
	}
	return &Internal{host: normalized, names: names}, nil
}

// Issuing is the registry with its token issuer and the projects' pull generations, as while app git builds are on.
func (in *Internal) Issuing(issuer *Issuer, generations PullGenerations) *Internal {
	issuing := *in
	issuing.issuer, issuing.generations = issuer, generations
	return &issuing
}

// errNotIssuing answers a pull credential asked of a registry that only guards.
var errNotIssuing = errors.New("the internal registry issues pull credentials only while app git builds are on")

// hostname is a registry host as compared: lowercase, no port, no trailing dot.
func hostname(host string) string {
	host = strings.ToLower(host)
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	return strings.TrimSuffix(strings.Trim(host, "[]"), ".")
}

// CanIssue is whether tokens and pull credentials exist, as they do while app git builds are on.
func (in *Internal) CanIssue() bool { return in != nil && in.issuer != nil && in.generations != nil }

// Host is the registry host images name; nil has none.
func (in *Internal) Host() string {
	if in == nil {
		return ""
	}
	return in.host
}

// Names answers whether image is on the internal registry, however its host is spelled.
func (in *Internal) Names(image string) bool {
	return in.NamesHost(apphost.ImageRegistry(image))
}

// NamesHost answers whether a registry host, with or without a port, is the internal registry.
func (in *Internal) NamesHost(host string) bool {
	return in != nil && in.names[hostname(host)]
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
	if in == nil {
		return "", ErrInternalImage
	}
	repository, err := Repository(projectID, appName)
	if err != nil {
		return "", err
	}
	if !digestPattern.MatchString(digest) {
		return "", fmt.Errorf("an internal image is deployed by its sha256 digest, not %q", digest)
	}
	return in.host + "/" + repository + "@" + digest, nil
}

// PullCredential is what the project's pull secret carries, at its current generation.
func (in *Internal) PullCredential(projectID string) (PullCredential, error) {
	if !in.CanIssue() {
		return PullCredential{}, errNotIssuing
	}
	gen, err := in.generations.PullGeneration(projectID)
	if err != nil {
		return PullCredential{}, err
	}
	return in.issuer.PullCredential(projectID, gen), nil
}

// Client is provisioning's own client for the registry at baseURL; only an issuing registry has one.
func (in *Internal) Client(baseURL string) (*Client, error) {
	if !in.CanIssue() {
		return nil, errNotIssuing
	}
	return NewClient(baseURL, in.issuer)
}

// VerifyPull checks a pull password against the project's current generation.
func (in *Internal) VerifyPull(projectID, password string) (bool, error) {
	if !in.CanIssue() {
		return false, errNotIssuing
	}
	gen, err := in.generations.PullGeneration(projectID)
	if err != nil {
		return false, err
	}
	return in.issuer.VerifyPull(projectID, gen, password), nil
}
