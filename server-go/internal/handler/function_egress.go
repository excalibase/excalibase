package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

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

// SetRuntimeEdge names the public edge's pods. With it, functions may call the
// platform's own API host: the worker is granted the host and the runtime's
// policy the edge pods its requests land on (EXC-558).
func (h *FunctionHandler) SetRuntimeEdge(edge k8s.EdgePeer) {
	h.runtimeEdge = edge
}

// SetRuntimeCiliumFQDN fences each project's runtime with a CiliumNetworkPolicy
// that admits its allowlist by host name (EXC-558).
func (h *FunctionHandler) SetRuntimeCiliumFQDN(on bool) {
	h.runtimeCiliumFQDN = on
}

// platformHosts is the API host the worker may call, only once the edge
// it is reached through is named; without one the call would hang at the policy.
func (h *FunctionHandler) platformHosts() []string {
	if len(h.runtimeEdge.Ports) == 0 {
		return nil
	}
	return platformEgressHosts(h.publicBaseURL)
}

// platformEgressHosts is the public base URL's host:port in allowlist form; a
// base that is not a public host (a local or private address) gives none.
func platformEgressHosts(publicBaseURL string) []string {
	base, err := url.Parse(publicBaseURL)
	if err != nil || base.Hostname() == "" {
		return nil
	}
	port := base.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[base.Scheme]
	}
	hosts, err := edgefn.ParseEgressHosts([]string{net.JoinHostPort(base.Hostname(), port)})
	if err != nil {
		return nil
	}
	return hosts
}

// workerEgressHosts is the worker's net permission: the effective list, the
// platform's hosts and the database EXCALIBASE_DB_URL names.
func (h *FunctionHandler) workerEgressHosts(projectID string) []string {
	return edgefn.MergeEgressHosts(h.effectiveEgressHosts(projectID), h.platformHosts(), h.databaseEgressHosts(projectID))
}

// databaseEgressHosts lets the worker open the URL it is given (EXC-558). The
// address is in-cluster, so it never enters the project's allowlist, which
// refuses those; the runtime's policy admits only the project's database pods.
func (h *FunctionHandler) databaseEgressHosts(projectID string) []string {
	target, ok := h.injectedDBTarget(projectID)
	if !ok {
		return nil
	}
	if !runtimeHostShape.MatchString(target.host) {
		log.Printf("WARN: functions of %s cannot be granted their database host %q: not a host name the runtime accepts", projectID, target.host)
		return nil
	}
	return []string{net.JoinHostPort(target.host, target.port)}
}

// runtimeHostShape is a host the runtime takes as a net permission; it refuses
// a whole deploy over any other.
var runtimeHostShape = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

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
		EffectiveHosts: edgefn.MergeEgressHosts(h.egressDefaults, projectHosts, h.platformHosts()),
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
		Image:           h.runtimeImage,
		RuntimeSecret:   edgefn.DeriveRuntimeSecret(h.runtimeSecret, projectID),
		Tier:            h.tierFor(projectID),
		AllowedHosts:    h.effectiveEgressHosts(projectID),
		PlatformHosts:   h.platformHosts(),
		Edge:            h.runtimeEdge,
		CiliumFQDN:      h.runtimeCiliumFQDN,
		ProvisioningURL: h.runtimeProvisioningURL,
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

// unreachableImports names the imports a per-project runtime could not fetch:
// its NetworkPolicy closes everything the egress allowlist does not open. The
// shared docker runtime is not fenced that way, so it is not checked.
func (h *FunctionHandler) unreachableImports(projectID, code string) []string {
	if h.k8sClient == nil {
		return nil
	}
	return edgefn.UnreachableImports(code, h.workerEgressHosts(projectID))
}

// unreachableImportsMessage refuses a deploy whose imports the runtime would
// hang fetching (EXC-560), and says the two ways out.
func unreachableImportsMessage(imports []string) string {
	return "the runtime does not carry " + strings.Join(imports, ", ") +
		" and this project's function egress does not allow fetching it: add the host to the egress allowlist," +
		" or put the code in the function's own files (zod and @excalibase/server are built in)"
}
