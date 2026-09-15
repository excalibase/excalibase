package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/byoc"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// ProvisionBYOC registers an externally managed database. The host and the
// DSN-bound fields go through the BYOC egress guard before anything is
// stored: see package byoc for the threat model (stored SSRF via internal /
// metadata / rebinding targets, DSN re-routing, operator allowlist). The same
// guard dials the stored target later, so registration-time validation is a
// fast fail for the user, not the only line of defence.
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

	creds := byoc.Credentials{Host: req.Host, Database: req.Database, Username: req.Username, Password: req.Password}
	if err := h.egress().ValidateCredentials(r.Context(), creds); err != nil {
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

// SetEgressGuard installs the operator-configured BYOC guard (allowlist +
// resolver). Without it the handler falls back to byoc.Default(), which still
// blocks every internal range.
func (h *ProvisioningHandler) SetEgressGuard(g *byoc.Guard) { h.egressGuard = g }

func (h *ProvisioningHandler) egress() *byoc.Guard {
	if h.egressGuard != nil {
		return h.egressGuard
	}
	return byoc.Default()
}

// validateBYOCHost is the host-only check, kept as a named seam for tests.
func (h *ProvisioningHandler) validateBYOCHost(ctx context.Context, host string) error {
	return h.egress().ValidateHost(ctx, host)
}
