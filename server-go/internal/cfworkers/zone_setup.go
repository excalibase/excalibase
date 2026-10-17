package cfworkers

import (
	"context"
	"net/http"
	"net/url"
)

// FallbackOrigin is where Cloudflare sends custom hostname traffic on the zone.
type FallbackOrigin struct {
	Origin string   `json:"origin"`
	Status string   `json:"status"`
	Errors []string `json:"errors"`
}

// DNSRecord is the part of a zone DNS record the setup reads and writes.
type DNSRecord struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	// TTL 1 is Cloudflare's "automatic".
	TTL int `json:"ttl,omitempty"`
}

// WorkerRoute maps a URL pattern on the zone to a Worker.
type WorkerRoute struct {
	ID      string `json:"id,omitempty"`
	Pattern string `json:"pattern"`
	Script  string `json:"script"`
}

// FallbackOrigin reads the zone's fallback origin; a zone without one answers an empty origin.
func (z *ZoneClient) FallbackOrigin(ctx context.Context) (FallbackOrigin, error) {
	var out FallbackOrigin
	err := z.send(ctx, http.MethodGet, z.zonePath("custom_hostnames", "fallback_origin"), nil, &out)
	if isNotFound(err) {
		return FallbackOrigin{}, nil
	}
	return out, err
}

func (z *ZoneClient) SetFallbackOrigin(ctx context.Context, origin string) (FallbackOrigin, error) {
	var out FallbackOrigin
	err := z.send(ctx, http.MethodPut, z.zonePath("custom_hostnames", "fallback_origin"), map[string]string{"origin": origin}, &out)
	return out, err
}

// DNSRecords answers the zone's records named exactly name.
func (z *ZoneClient) DNSRecords(ctx context.Context, name string) ([]DNSRecord, error) {
	target := z.zonePath("dns_records") + "?" + url.Values{"name.exact": {name}}.Encode()
	var listed []DNSRecord
	if err := z.send(ctx, http.MethodGet, target, nil, &listed); err != nil {
		return nil, err
	}
	matching := []DNSRecord{}
	for _, record := range listed {
		if record.Name == name {
			matching = append(matching, record)
		}
	}
	return matching, nil
}

// CreateDNSRecord adds record with an automatic TTL.
func (z *ZoneClient) CreateDNSRecord(ctx context.Context, record DNSRecord) (DNSRecord, error) {
	record.ID = ""
	record.TTL = 1
	var out DNSRecord
	err := z.send(ctx, http.MethodPost, z.zonePath("dns_records"), record, &out)
	return out, err
}

func (z *ZoneClient) WorkerRoutes(ctx context.Context) ([]WorkerRoute, error) {
	var listed []WorkerRoute
	err := z.send(ctx, http.MethodGet, z.zonePath("workers", "routes"), nil, &listed)
	return listed, err
}

func (z *ZoneClient) CreateWorkerRoute(ctx context.Context, pattern, script string) (WorkerRoute, error) {
	var out WorkerRoute
	err := z.send(ctx, http.MethodPost, z.zonePath("workers", "routes"), WorkerRoute{Pattern: pattern, Script: script}, &out)
	return out, err
}
