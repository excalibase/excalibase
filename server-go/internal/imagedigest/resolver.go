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
	// routes are registries the platform may dial on a private address, by the host images name
	// them with; internal is the client that dials only those (EXC-732).
	routes   map[string]RegistryRoute
	internal *http.Client
}

// RegistryRoute is a registry on a private address the platform is told to reach: the host images
// name it by, where it is actually dialled (host:port) and whether it speaks plain HTTP.
type RegistryRoute struct {
	Name      string
	Endpoint  string
	PlainHTTP bool
}

// NewResolver reaches registries only on public addresses, checked on the
// address actually dialled, so neither a registry host nor a token realm can
// point the control plane at the cluster's own network.
//
// deny adds the install's own node and load-balancer ranges to what is refused.
func NewResolver(deny []netip.Prefix) *Resolver {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: refuseInternalOrDenied(deny)}
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

// NewResolverWithRoutes is NewResolver plus a second client that may dial exactly the routes'
// endpoints and names, and nothing else, for reading an image's volumes. Resolve never uses it.
func NewResolverWithRoutes(deny []netip.Prefix, routes []RegistryRoute) *Resolver {
	resolver := NewResolver(deny)
	resolver.routes = map[string]RegistryRoute{}
	allowed := map[string]bool{}
	for _, route := range routes {
		resolver.routes[route.Name] = route
		allowed[route.Endpoint] = true
		allowed[withDefaultPort(route.Name, route.PlainHTTP)] = true
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	resolver.internal = &http.Client{Timeout: resolveTimeout, Transport: &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if !allowed[address] {
				return nil, publicaddr.ErrInternalAddress
			}
			return dialer.DialContext(ctx, network, address)
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		MaxIdleConns:          4,
		IdleConnTimeout:       60 * time.Second,
	}}
	return resolver
}

func withDefaultPort(host string, plain bool) string {
	if strings.Contains(host, ":") {
		return host
	}
	if plain {
		return host + ":80"
	}
	return host + ":443"
}

// Resolve answers the digest image names right now. cred, when set, is sent
// only to the image's own registry.
func (r *Resolver) Resolve(ctx context.Context, image string, cred *apphost.RegistryCredential) (string, error) {
	repo, reference, err := r.repository(image, cred)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	desc, err := repo.Resolve(ctx, reference)
	if err != nil {
		return "", classify(image, err)
	}
	return desc.Digest.String(), nil
}

// repository is the image's repository on its registry, signed in with cred when one is given,
// and the tag or digest to ask it for.
func (r *Resolver) repository(image string, cred *apphost.RegistryCredential) (*remote.Repository, string, error) {
	return r.repositoryVia(image, cred, false)
}

// repositoryVia is repository, or with internal set the same on a configured private registry:
// dialled at its endpoint by the client that may reach only those.
func (r *Resolver) repositoryVia(image string, cred *apphost.RegistryCredential, internal bool) (*remote.Repository, string, error) {
	if err := apphost.ValidateImageReference(image); err != nil {
		return nil, "", err
	}
	location, reference := repositoryLocation(image)
	httpClient, plainHTTP := r.client, r.plainHTTP
	if route, found := r.routes[apphost.ImageRegistry(image)]; internal && found {
		_, path, _ := strings.Cut(location, "/")
		location, httpClient, plainHTTP = route.Endpoint+"/"+path, r.internal, route.PlainHTTP
	}
	repo, err := remote.NewRepository(location)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", apphost.ErrInvalidImage, err)
	}
	repo.PlainHTTP = plainHTTP
	client := &auth.Client{Client: httpClient, Cache: auth.NewCache()}
	if cred != nil {
		client.Credential = auth.StaticCredential(repo.Reference.Registry,
			auth.Credential{Username: cred.Username, Password: cred.Password})
	}
	repo.Client = client
	return repo, reference, nil
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

func refuseInternalOrDenied(deny []netip.Prefix) func(string, string, syscall.RawConn) error {
	checker := publicaddr.NewDialer(nil, deny)
	return func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return publicaddr.ErrInternalAddress
		}
		addr, err := netip.ParseAddr(host)
		if err != nil {
			return publicaddr.ErrInternalAddress
		}
		return checker.Check(addr)
	}
}
