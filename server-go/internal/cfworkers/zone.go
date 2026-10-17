package cfworkers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Zone calls are small JSON requests; a minute is ample.
const zoneRequestTimeout = time.Minute

// zoneIDPattern is a Cloudflare zone id: 32 hex characters.
var zoneIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ZoneConfig names the user-content zone custom hostnames live on. APIToken
// is a secret: it is sent only in the Authorization header.
type ZoneConfig struct {
	BaseURL  string
	APIToken string
	ZoneID   string
	HTTP     *http.Client
}

// ZoneClient calls the zone-level API: Cloudflare for SaaS custom hostnames,
// the fallback origin, DNS records and Worker routes.
type ZoneClient struct {
	api  *Client
	zone string
}

// NewZoneClient refuses a missing token or a zone id that is not Cloudflare's shape.
func NewZoneClient(cfg ZoneConfig) (*ZoneClient, error) {
	if cfg.APIToken == "" {
		return nil, errors.New("cloudflare zone client needs an API token")
	}
	if !zoneIDPattern.MatchString(cfg.ZoneID) {
		return nil, errors.New("cloudflare zone client needs a zone id of 32 hex characters")
	}
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	httpClient := cfg.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: zoneRequestTimeout}
	}
	return &ZoneClient{api: &Client{baseURL: baseURL, token: cfg.APIToken, http: httpClient}, zone: cfg.ZoneID}, nil
}

func (z *ZoneClient) zonePath(parts ...string) string {
	escaped := make([]string, 0, len(parts)+4)
	escaped = append(escaped, "client", "v4", "zones", z.zone)
	for _, part := range parts {
		escaped = append(escaped, url.PathEscape(part))
	}
	return z.api.baseURL + "/" + strings.Join(escaped, "/")
}

// send encodes body as JSON (nil sends none) and decodes the envelope's result into out (nil ignores it).
func (z *ZoneClient) send(ctx context.Context, method, target string, body, out any) error {
	contentType := ""
	var reader io.Reader
	if body != nil {
		encoded, err := marshalJSON(body)
		if err != nil {
			return err
		}
		reader = strings.NewReader(encoded)
		contentType = "application/json"
	}
	_, raw, err := z.api.call(ctx, method, target, z.api.token, contentType, reader)
	if err != nil || out == nil || len(raw) == 0 || string(raw) == "null" {
		return err
	}
	if err := unmarshalJSON(raw, out); err != nil {
		return fmt.Errorf("decode cloudflare answer: %w", err)
	}
	return nil
}

// isNotFound is Cloudflare answering 404.
func isNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}
