// Package imagedigest asks an image's registry which digest a tag names, so a
// deploy runs exactly that build however the tag moves afterwards (EXC-543).
package imagedigest

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/publicaddr"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/errcode"
)

var (
	ErrNotFound    = errors.New("the registry has no such image")
	ErrDenied      = errors.New("the registry refused access to the image; save a credential for its registry")
	ErrRateLimited = errors.New("the registry is rate limiting requests; try again later")
	ErrUnavailable = errors.New("the registry did not answer")
	ErrNotPublic   = errors.New("the image's registry is not a public address")
)

// Docker Hub serves its API from another host than the one images are named by.
const dockerHubAPI = "registry-1.docker.io"

const resolveTimeout = 20 * time.Second

type Resolver struct {
	client    *http.Client
	plainHTTP bool
}

// NewResolver reaches registries only on public addresses, checked on the
// address actually dialled, so neither a registry host nor a token realm can
// point the control plane at the cluster's own network.
func NewResolver() *Resolver {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: refuseInternalAddress}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		MaxIdleConns:          8,
		IdleConnTimeout:       60 * time.Second,
	}
	return newResolver(&http.Client{Transport: transport, Timeout: resolveTimeout}, false)
}

func newResolver(client *http.Client, plainHTTP bool) *Resolver {
	return &Resolver{client: client, plainHTTP: plainHTTP}
}

// Resolve answers the digest image names right now. cred, when set, is sent
// only to the image's own registry.
func (r *Resolver) Resolve(ctx context.Context, image string, cred *apphost.RegistryCredential) (string, error) {
	if err := apphost.ValidateImageReference(image); err != nil {
		return "", err
	}
	location, reference := repositoryLocation(image)
	repo, err := remote.NewRepository(location)
	if err != nil {
		return "", fmt.Errorf("%w: %v", apphost.ErrInvalidImage, err)
	}
	repo.PlainHTTP = r.plainHTTP
	client := &auth.Client{Client: r.client, Cache: auth.NewCache()}
	if cred != nil {
		client.Credential = auth.StaticCredential(repo.Reference.Registry,
			auth.Credential{Username: cred.Username, Password: cred.Password})
	}
	repo.Client = client
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	desc, err := repo.Resolve(ctx, reference)
	if err != nil {
		return "", classify(image, err)
	}
	return desc.Digest.String(), nil
}

// repositoryLocation is where the registry API serves the image's repository,
// and the tag or digest to ask it for.
func repositoryLocation(image string) (string, string) {
	name, reference := splitReference(image)
	host := apphost.ImageRegistry(image)
	path := name
	if first, rest, found := strings.Cut(name, "/"); found && (strings.ContainsAny(first, ".:") || first == "localhost") {
		path = rest
	}
	if host == "docker.io" {
		host = dockerHubAPI
		if !strings.Contains(path, "/") {
			path = "library/" + path
		}
	}
	return host + "/" + path, reference
}

func splitReference(image string) (string, string) {
	if name, digest, found := strings.Cut(image, "@"); found {
		return name, digest
	}
	colon := strings.LastIndex(image, ":")
	return image[:colon], image[colon+1:]
}

func classify(image string, err error) error {
	var response *errcode.ErrorResponse
	switch {
	case errors.Is(err, publicaddr.ErrInternalAddress):
		return fmt.Errorf("%w: %s", ErrNotPublic, apphost.ImageRegistry(image))
	case errors.Is(err, errdef.ErrNotFound):
		return fmt.Errorf("%w: %s", ErrNotFound, image)
	case errors.As(err, &response):
		return classifyStatus(image, response.StatusCode)
	default:
		log.Printf("resolve %s: %v", image, err)
		return fmt.Errorf("%w: %s", ErrUnavailable, apphost.ImageRegistry(image))
	}
}

func classifyStatus(image string, status int) error {
	switch {
	case status == http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrNotFound, image)
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("%w (%s)", ErrDenied, apphost.ImageRegistry(image))
	case status == http.StatusTooManyRequests:
		return fmt.Errorf("%w (%s)", ErrRateLimited, apphost.ImageRegistry(image))
	default:
		return fmt.Errorf("%w: %s answered %d", ErrUnavailable, apphost.ImageRegistry(image), status)
	}
}

func refuseInternalAddress(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return publicaddr.ErrInternalAddress
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return publicaddr.ErrInternalAddress
	}
	return publicaddr.ClassifyAddr(addr)
}
