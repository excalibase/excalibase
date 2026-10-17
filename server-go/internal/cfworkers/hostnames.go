package cfworkers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
)

// Domain control validation methods this client asks for. Email is never
// used: it would mail the customer's WHOIS contacts on our behalf.
const (
	ValidationHTTP = "http"
	ValidationTXT  = "txt"
)

// duplicateHostnameCode is Cloudflare refusing a hostname the zone already holds.
const duplicateHostnameCode = 1406

// customHostnameIDPattern is the 36-character id Cloudflare gives a custom hostname.
var customHostnameIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// CustomHostname is the part of Cloudflare's custom hostname object Sites reads.
type CustomHostname struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	// Status is the hostname's activation: "active" once Cloudflare proxies it.
	Status                    string                `json:"status"`
	VerificationErrors        []string              `json:"verification_errors"`
	OwnershipVerification     OwnershipVerification `json:"ownership_verification"`
	OwnershipVerificationHTTP OwnershipHTTP         `json:"ownership_verification_http"`
	SSL                       HostnameSSL           `json:"ssl"`
}

// OwnershipVerification is the TXT record that proves the hostname before its CNAME is in place.
type OwnershipVerification struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// OwnershipHTTP is the same proof served over HTTP instead.
type OwnershipHTTP struct {
	HTTPURL  string `json:"http_url"`
	HTTPBody string `json:"http_body"`
}

// HostnameSSL is the certificate Cloudflare issues and renews for the hostname.
type HostnameSSL struct {
	// Status is "active" once the certificate is deployed.
	Status            string             `json:"status"`
	Method            string             `json:"method"`
	ValidationRecords []ValidationRecord `json:"validation_records"`
	ValidationErrors  []ValidationError  `json:"validation_errors"`
}

// ValidationRecord is one DCV token: a TXT record or an HTTP file, depending on the method.
type ValidationRecord struct {
	TXTName  string `json:"txt_name"`
	TXTValue string `json:"txt_value"`
	HTTPURL  string `json:"http_url"`
	HTTPBody string `json:"http_body"`
	Status   string `json:"status"`
}

type ValidationError struct {
	Message string `json:"message"`
}

type sslRequest struct {
	Method string `json:"method"`
	Type   string `json:"type"`
}

type createHostnameRequest struct {
	Hostname string     `json:"hostname"`
	SSL      sslRequest `json:"ssl"`
}

type editHostnameRequest struct {
	SSL sslRequest `json:"ssl"`
}

func validationRequest(method string) (sslRequest, error) {
	if method != ValidationHTTP && method != ValidationTXT {
		return sslRequest{}, fmt.Errorf("unsupported validation method %q", method)
	}
	return sslRequest{Method: method, Type: "dv"}, nil
}

func (z *ZoneClient) hostnamePath(id string) (string, error) {
	if !customHostnameIDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid custom hostname id %q", id)
	}
	return z.zonePath("custom_hostnames", id), nil
}

// CreateCustomHostname adds hostname to the zone with a DV certificate validated by method.
func (z *ZoneClient) CreateCustomHostname(ctx context.Context, hostname, method string) (CustomHostname, error) {
	ssl, err := validationRequest(method)
	if err != nil {
		return CustomHostname{}, err
	}
	var out CustomHostname
	err = z.send(ctx, http.MethodPost, z.zonePath("custom_hostnames"), createHostnameRequest{Hostname: hostname, SSL: ssl}, &out)
	return out, err
}

// IsDuplicateCustomHostname is Cloudflare answering that the zone already holds the hostname.
func IsDuplicateCustomHostname(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && slices.Contains(apiErr.Codes, duplicateHostnameCode)
}

func (z *ZoneClient) GetCustomHostname(ctx context.Context, id string) (CustomHostname, error) {
	target, err := z.hostnamePath(id)
	if err != nil {
		return CustomHostname{}, err
	}
	var out CustomHostname
	err = z.send(ctx, http.MethodGet, target, nil, &out)
	return out, err
}

// RevalidateCustomHostname sends the same SSL settings again, which Cloudflare
// takes as a request to run domain control validation now.
func (z *ZoneClient) RevalidateCustomHostname(ctx context.Context, id, method string) (CustomHostname, error) {
	target, err := z.hostnamePath(id)
	if err != nil {
		return CustomHostname{}, err
	}
	ssl, err := validationRequest(method)
	if err != nil {
		return CustomHostname{}, err
	}
	var out CustomHostname
	err = z.send(ctx, http.MethodPatch, target, editHostnameRequest{SSL: ssl}, &out)
	return out, err
}

// FindCustomHostname answers the zone's custom hostname named exactly hostname, nil when none.
func (z *ZoneClient) FindCustomHostname(ctx context.Context, hostname string) (*CustomHostname, error) {
	target := z.zonePath("custom_hostnames") + "?" + url.Values{"hostname.exact": {hostname}}.Encode()
	var listed []CustomHostname
	if err := z.send(ctx, http.MethodGet, target, nil, &listed); err != nil {
		return nil, err
	}
	// The filter is Cloudflare's; the match is checked here too, so a loose filter cannot hand back another name.
	for _, host := range listed {
		if host.Hostname == hostname {
			return &host, nil
		}
	}
	return nil, nil
}

// DeleteCustomHostname removes the hostname and revokes its certificates; one already gone counts as deleted.
func (z *ZoneClient) DeleteCustomHostname(ctx context.Context, id string) error {
	target, err := z.hostnamePath(id)
	if err != nil {
		return err
	}
	err = z.send(ctx, http.MethodDelete, target, nil, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

func marshalJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode cloudflare request: %w", err)
	}
	return string(encoded), nil
}

func unmarshalJSON(raw []byte, out any) error { return json.Unmarshal(raw, out) }
