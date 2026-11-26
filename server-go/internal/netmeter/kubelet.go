package netmeter

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const kubeletTimeout = 15 * time.Second

// KubeletFetcher reads a node's kubelet summary over HTTPS with the service
// account's token. The kubelet's serving certificate is checked against a CA
// file; there is no way to skip that check.
type KubeletFetcher struct {
	client    *http.Client
	tokenFile string
	port      string
}

// NewKubeletFetcher fails when the CA file is missing or holds no certificate.
func NewKubeletFetcher(caFile, tokenFile, port string) (*KubeletFetcher, error) {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("kubelet CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("kubelet CA file holds no certificate")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	return &KubeletFetcher{client: &http.Client{Transport: transport, Timeout: kubeletTimeout}, tokenFile: tokenFile, port: port}, nil
}

// Summary asks the kubelet at a node address for its pods' counters. The
// token is read each call, since projected tokens rotate.
func (f *KubeletFetcher) Summary(ctx context.Context, node string) ([]PodNet, error) {
	token, err := os.ReadFile(f.tokenFile)
	if err != nil {
		return nil, fmt.Errorf("service account token: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+net.JoinHostPort(node, f.port)+"/stats/summary", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	response, err := f.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("kubelet %s: %w", node, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kubelet %s answered %d", node, response.StatusCode)
	}
	return ParseSummary(response.Body)
}
