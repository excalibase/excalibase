package mcpserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"
)

const edgeDialTimeout = 10 * time.Second

// EdgeRoute sends the platform's own hostnames to the edge in front of them (single host): inside the
// stack their public names may resolve nowhere, as *.localhost does (EXC-608).
type EdgeRoute struct {
	// TLSAddress is where the edge serves HTTPS (edge:443); plain HTTP goes to the same host on port 80.
	TLSAddress string
	// Hosts are the platform's hostnames; Domain covers every app host (<app>.<Domain>).
	Hosts  []string
	Domain string
	// Root reads the edge's own CA root when it issues locally; nil trusts the system's roots only.
	Root func(context.Context) ([]byte, error)
}

func (e *EdgeRoute) serves(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return slices.Contains(e.Hosts, host) || (e.Domain != "" && strings.HasSuffix(host, "."+e.Domain))
}

func (e *EdgeRoute) address(port string) string {
	if port == "443" {
		return e.TLSAddress
	}
	host, _, err := net.SplitHostPort(e.TLSAddress)
	if err != nil {
		return e.TLSAddress
	}
	return net.JoinHostPort(host, port)
}

// probeClient is the client every probe of the platform's own addresses uses: the default one, or on a
// single host one that dials the edge for its names and trusts its own CA as well as the system's.
func (s Settings) probeClient(ctx context.Context) (*http.Client, error) {
	client := &http.Client{CheckRedirect: noRedirects}
	if s.Edge == nil {
		return client, nil
	}
	defaults, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("the default HTTP transport is not an *http.Transport")
	}
	transport := defaults.Clone()
	// Straight to the edge: an environment proxy would see the public name and route around it.
	transport.Proxy = nil
	dialer := &net.Dialer{Timeout: edgeDialTimeout}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if host, port, err := net.SplitHostPort(address); err == nil && s.Edge.serves(host) {
			address = s.Edge.address(port)
		}
		return dialer.DialContext(ctx, network, address)
	}
	if s.Edge.Root != nil {
		roots, err := edgeRoots(ctx, s.Edge.Root)
		if err != nil {
			return nil, err
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	client.Transport = transport
	return client, nil
}

func edgeRoots(ctx context.Context, read func(context.Context) ([]byte, error)) (*x509.CertPool, error) {
	root, err := read(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the edge's CA root: %w", err)
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(root) {
		return nil, errors.New("the edge's CA root is not a PEM certificate")
	}
	return roots, nil
}
