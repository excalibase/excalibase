package netmeter

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const kubeletTimeout = 15 * time.Second

// KubeletFetcher reads a node's kubelet summary over HTTPS with the service
// account's token. The kubelet's serving certificate is checked against a CA
// file, which is read again whenever the file changes, so a CA rotation needs
// no restart; there is no way to skip the check, and a redirect is never
// followed, so the token goes to the kubelet and nowhere else.
type KubeletFetcher struct {
	caFile    string
	tokenFile string
	port      string

	mu       sync.Mutex
	client   *http.Client
	loadedAt time.Time
	loadedSz int64
}

// NewKubeletFetcher fails when the CA file is missing or holds no certificate.
func NewKubeletFetcher(caFile, tokenFile, port string) (*KubeletFetcher, error) {
	fetcher := &KubeletFetcher{caFile: caFile, tokenFile: tokenFile, port: port}
	if err := fetcher.loadCA(); err != nil {
		return nil, err
	}
	return fetcher, nil
}

func (f *KubeletFetcher) loadCA() error {
	info, err := os.Stat(f.caFile)
	if err != nil {
		return fmt.Errorf("kubelet CA: %w", err)
	}
	pem, err := os.ReadFile(f.caFile)
	if err != nil {
		return fmt.Errorf("kubelet CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return errors.New("kubelet CA file holds no certificate")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	f.client = &http.Client{
		Transport: transport, Timeout: kubeletTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	f.loadedAt, f.loadedSz = info.ModTime(), info.Size()
	return nil
}

// current is the client for this call: rebuilt when the CA file changed. A
// replacement that cannot be read keeps the last good CA and is logged.
func (f *KubeletFetcher) current() *http.Client {
	f.mu.Lock()
	defer f.mu.Unlock()
	if info, err := os.Stat(f.caFile); err == nil && (!info.ModTime().Equal(f.loadedAt) || info.Size() != f.loadedSz) {
		if err := f.loadCA(); err != nil {
			log.Printf("net meter: the kubelet CA changed and cannot be used, keeping the last one: %v", err)
			// Remember the bad file so it is not tried and logged every round.
			f.loadedAt, f.loadedSz = info.ModTime(), info.Size()
		}
	}
	return f.client
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
	response, err := f.current().Do(request)
	if err != nil {
		return nil, fmt.Errorf("kubelet %s: %w", node, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kubelet %s answered %d", node, response.StatusCode)
	}
	return ParseSummary(response.Body)
}
