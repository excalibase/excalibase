package handler

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func (h *ProvisioningHandler) ProvisionBYOC(w http.ResponseWriter, r *http.Request) {
	var req domain.BYOCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.ProjectName == "" || req.Host == "" || req.Port == 0 ||
		req.Database == "" || req.Username == "" || req.Password == "" || req.OrgID == "" {
		httpError(w, "projectName, orgId, host, port, database, username, and password are required", http.StatusBadRequest)
		return
	}

	if err := validateBYOCHost(req.Host); err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Port < 1 || req.Port > 65535 {
		httpError(w, "port must be between 1 and 65535", http.StatusBadRequest)
		return
	}

	resp, err := h.svc.ProvisionBYOC(r.Context(), req)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, resp)
}

// lookupHost is overridable in tests so we can exercise the resolve-and-block
// path without depending on real DNS. Defaults to the stdlib resolver.
var lookupHost = net.LookupHost

// validateBYOCHost rejects host values that point at the platform itself or
// any internal network. BYOC credentials are stored and later used by the
// platform's own outbound connections (schema browser, realtime, edge fns),
// so accepting `127.0.0.1`, `::1`, RFC-1918 ranges, link-local, or the AWS/
// GCP metadata endpoint creates a stored-SSRF primitive: any user can pivot
// to internal services.
//
// For IP literals we classify directly. For DNS names we resolve at
// validation time (net.LookupHost) and reject if ANY resolved address is
// internal — this closes the DNS-rebinding gap where a hostname with a public
// A record passes validation but later re-resolves to an internal/metadata IP.
// Resolution failures fail closed (rejected).
//
// NOTE: this is resolve-and-block at registration time and leaves a TOCTOU
// window — the name could re-resolve to an internal IP at connect time.
// Fully closing it requires pinning the resolved public IP and dialing that
// pinned address when the platform later connects.
func validateBYOCHost(host string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return fmt.Errorf("host must not be empty")
	}
	// IP literal: classify directly.
	if ip := net.ParseIP(host); ip != nil {
		if err := classifyBYOCIP(ip); err != nil {
			return fmt.Errorf("host %q %s", host, err)
		}
		return nil
	}

	// DNS name: reject obviously non-routable names up front, then resolve
	// and block any internal address.
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") ||
		strings.HasSuffix(lower, ".internal") || strings.HasSuffix(lower, ".local") {
		return fmt.Errorf("host %q resolves inside the platform network", host)
	}

	addrs, err := lookupHost(host)
	if err != nil {
		// Fail closed: if we can't resolve the name, we can't prove it's
		// public, so refuse to store it.
		return fmt.Errorf("host %q could not be resolved: %v", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("host %q resolved to no addresses", host)
	}
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip == nil {
			return fmt.Errorf("host %q resolved to an unparseable address %q", host, addr)
		}
		if err := classifyBYOCIP(ip); err != nil {
			return fmt.Errorf("host %q resolves to %s; BYOC requires a publicly reachable host", host, addr)
		}
	}
	return nil
}

// classifyBYOCIP returns a non-nil error describing why an IP is not an
// acceptable BYOC target (loopback/private/link-local/unspecified/multicast/
// metadata). Returns nil for routable public addresses. Shared by the IP-literal
// and DNS-resolution paths so the block-list lives in exactly one place.
func classifyBYOCIP(ip net.IP) error {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("is not a routable public address")
	}
	if ip.IsPrivate() {
		return fmt.Errorf("is in a private network range")
	}
	// Block well-known cloud metadata endpoints as a belt-and-braces measure
	// (IsLinkLocalUnicast already covers 169.254.0.0/16 but make it explicit).
	if ip.Equal(net.IPv4(169, 254, 169, 254)) {
		return fmt.Errorf("is a cloud metadata endpoint")
	}
	return nil
}
