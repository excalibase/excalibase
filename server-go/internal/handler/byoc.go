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

// validateBYOCHost rejects host values that point at the platform itself or
// any internal network. BYOC credentials are stored and later used by the
// platform's own outbound connections (schema browser, realtime, edge fns),
// so accepting `127.0.0.1`, `::1`, RFC-1918 ranges, link-local, or the AWS/
// GCP metadata endpoint creates a stored-SSRF primitive: any user can pivot
// to internal services. Allowlists DNS hostnames; only checks IP literals.
func validateBYOCHost(host string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return fmt.Errorf("host must not be empty")
	}
	// If host is a DNS name, defer to DNS-time policy (operator's network).
	// If it's an IP literal, refuse private/loopback/link-local/metadata.
	ip := net.ParseIP(host)
	if ip == nil {
		// Reject obvious non-routable hostnames.
		lower := strings.ToLower(host)
		if lower == "localhost" || strings.HasSuffix(lower, ".localhost") ||
			strings.HasSuffix(lower, ".internal") || strings.HasSuffix(lower, ".local") {
			return fmt.Errorf("host %q resolves inside the platform network", host)
		}
		return nil
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("host %q is not a routable public address", host)
	}
	if ip.IsPrivate() {
		return fmt.Errorf("host %q is in a private network range; BYOC requires a publicly reachable host", host)
	}
	// Block well-known cloud metadata endpoints as a belt-and-braces measure
	// (IsLinkLocalUnicast already covers 169.254.0.0/16 but make it explicit).
	if ip.Equal(net.IPv4(169, 254, 169, 254)) {
		return fmt.Errorf("host %q is a cloud metadata endpoint", host)
	}
	return nil
}
