package dockerapps

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	edgeCATimeout = 5 * time.Second
	edgeCAMaxSize = 64 << 10
)

// EdgeCAReader reads the root of the edge's own CA from address, an unpublished listener the edge
// serves it on (EXCALIBASE_TLS=internal). Only a CA certificate is answered.
func EdgeCAReader(address string) func(context.Context) ([]byte, error) {
	client := &http.Client{Timeout: edgeCATimeout}
	return func(ctx context.Context) ([]byte, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if err != nil {
			return nil, err
		}
		answer, err := client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("the edge's CA root at %s: %w", address, err)
		}
		defer answer.Body.Close()
		if answer.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("the edge's CA root at %s: HTTP %d", address, answer.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(answer.Body, edgeCAMaxSize))
		if err != nil {
			return nil, fmt.Errorf("the edge's CA root at %s: %w", address, err)
		}
		block, _ := pem.Decode(body)
		if block == nil || block.Type != "CERTIFICATE" {
			return nil, errors.New("the edge's CA root is not a PEM certificate")
		}
		root, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !root.IsCA {
			return nil, errors.New("the edge's CA root is not a CA certificate")
		}
		return pem.EncodeToMemory(block), nil
	}
}
