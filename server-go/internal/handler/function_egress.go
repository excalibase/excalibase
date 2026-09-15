package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/go-chi/chi/v5"
)

// errEgressNotConfigured is returned when no EgressStore is wired — the
// allowlist API fails closed rather than pretending a setting was saved.
const errEgressNotConfigured = "egress allowlist not configured"

// egressResponse is the wire shape of GET/PUT /functions/egress.
//
//	allowedHosts   — the project's own setting (what PUT writes)
//	defaultHosts   — the operator default every project gets (read-only)
//	effectiveHosts — the union actually rendered into the runtime
type egressResponse struct {
	AllowedHosts   []string `json:"allowedHosts"`
	DefaultHosts   []string `json:"defaultHosts"`
	EffectiveHosts []string `json:"effectiveHosts"`
}

// SetEgressStore wires the per-project outbound allowlist store (EXC-348).
func (h *FunctionHandler) SetEgressStore(s edgefn.EgressStore) {
	h.egressStore = s
}

// SetEgressDefaults sets the operator-level allowlist merged into every
// project's effective list. Entries must already be parsed
// (edgefn.ParseEgressHostList).
func (h *FunctionHandler) SetEgressDefaults(hosts []string) {
	h.egressDefaults = edgefn.MergeEgressHosts(hosts)
}

// GetEgress serves GET /api/projects/{projectId}/functions/egress.
func (h *FunctionHandler) GetEgress(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	if err := edgefn.ValidateProjectID(projectID); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	if h.egressStore == nil {
		httpError(w, errEgressNotConfigured, http.StatusServiceUnavailable)
		return
	}
	hosts, err := h.egressStore.GetEgressHosts(projectID)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, h.egressResponseFor(hosts))
}

// PutEgress serves PUT /api/projects/{projectId}/functions/egress with body
// {"allowedHosts": [...]}. The list replaces the project's setting; it is
// validated before anything is written, then rendered into the live runtime
// (pod env + NetworkPolicy on k8s) and every function is redeployed so the
// workers pick up the new net permission.
func (h *FunctionHandler) PutEgress(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	if err := edgefn.ValidateProjectID(projectID); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	if h.egressStore == nil {
		httpError(w, errEgressNotConfigured, http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var body struct {
		AllowedHosts []string `json:"allowedHosts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, errInvalidRequestBody, http.StatusBadRequest)
		return
	}
	hosts, err := edgefn.ParseEgressHosts(body.AllowedHosts)
	if err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	if err := h.egressStore.SetEgressHosts(projectID, hosts); err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	if err := h.renderEgress(r.Context(), projectID); err != nil {
		// The setting is saved; the runtime catches up on the next deploy or
		// replay. Surface the rollout failure without failing the write.
		log.Printf("WARN: render egress for %s: %v", projectID, err)
	}
	h.redeployAll(r, projectID)
	writeJSON(w, h.egressResponseFor(hosts))
}

func (h *FunctionHandler) egressResponseFor(projectHosts []string) egressResponse {
	return egressResponse{
		AllowedHosts:   edgefn.MergeEgressHosts(projectHosts),
		DefaultHosts:   edgefn.MergeEgressHosts(h.egressDefaults),
		EffectiveHosts: edgefn.MergeEgressHosts(h.egressDefaults, projectHosts),
	}
}

// effectiveEgressHosts is the list rendered into the runtime: operator
// defaults plus the project's setting. A store read failure degrades to the
// defaults only — never to "everything".
func (h *FunctionHandler) effectiveEgressHosts(projectID string) []string {
	if h.egressStore == nil {
		return edgefn.MergeEgressHosts(h.egressDefaults)
	}
	hosts, err := h.egressStore.GetEgressHosts(projectID)
	if err != nil {
		log.Printf("WARN: read egress allowlist for %s: %v", projectID, err)
		return edgefn.MergeEgressHosts(h.egressDefaults)
	}
	return edgefn.MergeEgressHosts(h.egressDefaults, hosts)
}

// denoRuntimeSpecFor assembles the per-project runtime spec (EXC-348 adds the
// allowlist; SEC-C5 keeps the master secret out of tenant pods).
func (h *FunctionHandler) denoRuntimeSpecFor(projectID string) k8s.DenoRuntimeSpec {
	return k8s.DenoRuntimeSpec{
		Image:         h.runtimeImage,
		RuntimeSecret: edgefn.DeriveRuntimeSecret(h.runtimeSecret, projectID),
		Tier:          h.tierFor(projectID),
		AllowedHosts:  h.effectiveEgressHosts(projectID),
	}
}

// renderEgress pushes the current allowlist into an existing per-project
// runtime. A project whose runtime has not been created yet (no function
// deployed) is left alone: the first deploy renders the setting. The shared
// docker runtime has no per-project pod — its workers get the allowlist on
// each deploy payload instead.
func (h *FunctionHandler) renderEgress(ctx context.Context, projectID string) error {
	if h.k8sClient == nil {
		return nil
	}
	namespace := h.namespaceFor(projectID)
	if namespace == "" {
		return errors.New("no namespace for project")
	}
	exists, err := h.k8sClient.GetDeployment(ctx, namespace, "deno-runtime")
	if err != nil || !exists {
		return err
	}
	return h.k8sClient.EnsureDenoRuntime(ctx, namespace, h.denoRuntimeSpecFor(projectID))
}
