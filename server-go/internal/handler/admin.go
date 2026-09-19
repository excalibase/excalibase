package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

// auditWriter narrows the audit dependency to the single method admin handler
// uses. Both sqlite and postgres stores expose LogAudit; we don't take the
// full AuditLogStore interface because it's drifted from the implementations.
type auditWriter interface {
	LogAudit(ctx context.Context, entry *domain.AuditEntry) error
}

// AdminHandler exposes platform-operator endpoints: cross-org project listing
// with live resource usage, force-deprovision (bypasses deletion_protection),
// cascade org revocation, and a Loki-backed logs proxy. All routes require the
// platform_admin role and write an audit entry on mutation.
type AdminHandler struct {
	provSvc   *service.ProvisioningService
	store     storage.InstanceStore
	orgStore  storage.OrgStore
	auditLog  auditWriter
	k8sClient k8s.KubeClient
	lokiURL   string
	prom      promQuerier // nil-safe — usage stats fall back to "unknown" if Prometheus is unreachable
}

// promQuerier is a tiny shim for the one Prometheus call we make from the
// admin handler (per-pod CPU + memory). Implemented as a free function in
// loki.go-style helper rather than a full Prom client to keep the surface
// minimal and easy to mock in tests.
type promQuerier interface {
	InstantValue(ctx context.Context, query string) (float64, error)
}

// NewAdminHandler wires the admin endpoints. lokiURL + prom are optional:
// when empty/nil the corresponding endpoints return 503.
func NewAdminHandler(provSvc *service.ProvisioningService, store storage.InstanceStore, orgStore storage.OrgStore, auditLog auditWriter, k8sClient k8s.KubeClient, lokiURL string, prom promQuerier) *AdminHandler {
	return &AdminHandler{
		provSvc:   provSvc,
		store:     store,
		orgStore:  orgStore,
		auditLog:  auditLog,
		k8sClient: k8sClient,
		lokiURL:   lokiURL,
		prom:      prom,
	}
}

// Routes mounts admin endpoints under /api/admin. Caller is expected to apply
// auth.RequireAuth at the parent route. Each route then requires its own
// permission: reads need only view_any, but destructive operations require a
// write/governance permission so a read-only role (platform_viewer) can never
// force-drop a project or revoke an org (SEC-C6).
func (h *AdminHandler) Routes(r chi.Router) {
	r.With(auth.RequirePermission(auth.PermViewAny)).Get("/projects", h.ListAllProjects)
	r.With(auth.RequirePermission(auth.PermViewAny)).Get("/logs", h.QueryLogs)
	r.With(auth.RequirePermission(auth.PermDelete)).Delete("/projects/{projectId}", h.ForceDropProject)
	r.With(auth.RequirePermission(auth.PermManageOrgs)).Delete("/orgs/{orgId}", h.RevokeOrg)
}

// ListAllProjects returns every project across every org with optional live
// CPU + memory usage. Without Prometheus the usage fields are absent so the
// frontend can render "—" rather than zero.
func (h *AdminHandler) ListAllProjects(w http.ResponseWriter, r *http.Request) {
	instances, err := h.store.FindAll()
	if err != nil {
		httpError(w, "list projects: "+safeError(err), http.StatusInternalServerError)
		return
	}

	out := make([]map[string]interface{}, 0, len(instances))
	for _, inst := range instances {
		row := map[string]interface{}{
			"projectId":          inst.ProjectID,
			"projectName":        inst.ProjectName,
			"orgId":              inst.OrgID,
			"namespace":          inst.Namespace,
			"status":             inst.Status,
			"tier":               inst.Tier,
			"dbType":             inst.DBType,
			"createdAt":          inst.CreatedAt,
			"deletionProtection": inst.DeletionProtection != nil && *inst.DeletionProtection,
		}
		if h.prom != nil {
			// Prometheus container_* metrics use the project pod label;
			// use a 5m rate so transients don't dominate.
			pod := inst.ProjectID + "-postgres-1"
			cpu, _ := h.prom.InstantValue(r.Context(),
				fmt.Sprintf(`rate(container_cpu_usage_seconds_total{namespace=%q, pod=%q, container="postgres"}[5m])`, inst.Namespace, pod))
			mem, _ := h.prom.InstantValue(r.Context(),
				fmt.Sprintf(`container_memory_working_set_bytes{namespace=%q, pod=%q, container="postgres"}`, inst.Namespace, pod))
			row["cpuCores"] = cpu
			row["memBytes"] = mem
		}
		out = append(out, row)
	}
	writeJSON(w, out)
}

// ForceDropProject deprovisions a project even when deletion_protection is on.
// Used by operators to clean up stuck/abandoned tenants. Always audited.
func (h *AdminHandler) ForceDropProject(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	user := auth.GetUser(r.Context())
	userID := ""
	if user != nil {
		userID = user.ID
	}

	inst, err := h.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		httpError(w, "project not found", http.StatusNotFound)
		return
	}
	opts, err := decodeDeprovisionOptions(r)
	if err != nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Force=true clears deletion_protection so service.Deprovision proceeds.
	// We persist the cleared flag because rolling back on a partial-deprovision
	// failure is correct: protection should not silently re-enable.
	if inst.DeletionProtection != nil && *inst.DeletionProtection {
		falseVal := false
		inst.DeletionProtection = &falseVal
		if err := h.store.Update(inst); err != nil {
			httpError(w, "clear deletion protection: "+safeError(err), http.StatusInternalServerError)
			return
		}
	}

	if err := h.provSvc.DeprovisionWithOptions(r.Context(), projectID, opts); err != nil {
		log.Printf("action=force_drop_project project=%s status=failed err=%v", projectID, err)
		writeDeprovisionError(w, err)
		return
	}

	h.auditFireAndForget(r, &domain.AuditEntry{
		UserID:     userID,
		Action:     "admin.force_drop_project",
		Resource:   "project",
		ResourceID: projectID,
		Details:    fmt.Sprintf("org=%s tier=%s deleteBackups=%t", inst.OrgID, inst.Tier, opts.DeleteBackups),
	})
	writeJSON(w, map[string]string{"status": "deprovisioned", "projectId": projectID})
}

// RevokeOrg cascade-deprovisions every project owned by the org, then deletes
// the org row. Used to evict abusive tenants. ?cascade=true is required to
// avoid foot-guns; without it we refuse so operators can't accidentally
// orphan projects.
func (h *AdminHandler) RevokeOrg(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("cascade") != "true" {
		httpError(w, "?cascade=true is required to confirm intent", http.StatusBadRequest)
		return
	}
	orgID := chi.URLParam(r, "orgId")
	userID := extractUserID(r)

	if h.orgStore == nil {
		httpError(w, "org store unavailable", http.StatusServiceUnavailable)
		return
	}
	org, err := h.orgStore.FindOrgByID(r.Context(), orgID)
	if err != nil || org == nil {
		httpError(w, "org not found", http.StatusNotFound)
		return
	}

	dropped, failed, err := h.deprovisionOrgProjects(r.Context(), orgID)
	if err != nil {
		// Without the project list the cascade cannot know what it would be
		// leaving behind, so nothing is deleted.
		log.Printf("action=revoke_org org=%s status=failed err=%v", orgID, err)
		httpError(w, "projects could not be listed; nothing was deleted", http.StatusInternalServerError)
		return
	}

	// Delete org row last — only after all projects are gone, so org listing
	// stays consistent if a project drop fails midway.
	if len(failed) == 0 {
		if err := h.orgStore.DeleteOrg(r.Context(), orgID); err != nil {
			httpError(w, "delete org: "+safeError(err), http.StatusInternalServerError)
			return
		}
	}

	h.auditFireAndForget(r, &domain.AuditEntry{
		UserID:     userID,
		Action:     "admin.revoke_org",
		Resource:   "org",
		ResourceID: orgID,
		Details:    fmt.Sprintf("dropped=%d failed=%d slug=%s", dropped, len(failed), org.Slug),
	})
	status := http.StatusOK
	if len(failed) > 0 {
		status = http.StatusMultiStatus
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"orgId":   orgID,
		"dropped": dropped,
		"failed":  failed,
	})
}

// deprovisionOrgProjects removes deletion protection and deprovisions every
// project belonging to the org. It returns how many were torn down and the
// ids of those that were not; a project that fails leaves its row in
// DELETING with the reason, and the caller keeps the org. The reasons go to
// the server log rather than the response — they name cluster and vault
// internals. An unreadable project list is an error, not an empty cascade.
func (h *AdminHandler) deprovisionOrgProjects(ctx context.Context, orgID string) (int, []string, error) {
	instances, err := h.store.FindAll()
	if err != nil {
		return 0, nil, fmt.Errorf("list projects: %w", err)
	}
	dropped := 0
	var failed []string
	for _, inst := range instances {
		if inst.OrgID != orgID {
			continue
		}
		if err := h.dropOrgProject(ctx, inst); err != nil {
			log.Printf("action=revoke_org org=%s project=%s status=failed err=%v", orgID, inst.ProjectID, err)
			failed = append(failed, inst.ProjectID)
			continue
		}
		dropped++
	}
	return dropped, failed, nil
}

func (h *AdminHandler) dropOrgProject(ctx context.Context, inst *domain.DatabaseInstance) error {
	if inst.DeletionProtection != nil && *inst.DeletionProtection {
		falseVal := false
		inst.DeletionProtection = &falseVal
		if err := h.store.Update(inst); err != nil {
			return fmt.Errorf("clear deletion protection: %w", err)
		}
	}
	return h.provSvc.Deprovision(ctx, inst.ProjectID)
}

func extractUserID(r *http.Request) string {
	if user := auth.GetUser(r.Context()); user != nil {
		return user.ID
	}
	return ""
}

// QueryLogs is a thin Loki LogQL proxy. We translate the friendly query
// params into a LogQL selector + filter, then forward the result. We do
// NOT pass arbitrary LogQL through — that would let any platform_admin
// accidentally write a query that returns every log line in the cluster.
//
// Params:
//
//	service   = auth | graphql | provisioning | watcher | deno | cnpg | all
//	projectId = optional, narrows to the project's namespace + cnpg cluster
//	since     = duration string (e.g. "15m", "1h", "24h"); default 15m
//	q         = optional regex applied via |~
//	limit     = max lines (default 200, max 5000)
func (h *AdminHandler) QueryLogs(w http.ResponseWriter, r *http.Request) {
	if h.lokiURL == "" {
		httpError(w, "log query unavailable: LOKI_URL not configured", http.StatusServiceUnavailable)
		return
	}

	q := r.URL.Query()
	service := q.Get("service")
	if service == "" {
		service = "all"
	}
	projectID := q.Get("projectId")
	since := q.Get("since")
	if since == "" {
		since = "15m"
	}
	logql, err := buildLogQL(service, projectID, q.Get("q"))
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	limit := 200
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 5000 {
			limit = n
		}
	}

	dur, err := time.ParseDuration(since)
	if err != nil {
		httpError(w, "invalid since: "+err.Error(), http.StatusBadRequest)
		return
	}
	end := time.Now()
	start := end.Add(-dur)

	u, _ := url.Parse(h.lokiURL + "/loki/api/v1/query_range")
	qs := u.Query()
	qs.Set("query", logql)
	qs.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	qs.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	qs.Set("limit", strconv.Itoa(limit))
	qs.Set("direction", "backward")
	u.RawQuery = qs.Encode()

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		httpError(w, "loki query: "+safeError(err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// buildLogQL maps the friendly params to a Loki selector. We always anchor
// on namespace + app to avoid accidental cluster-wide scans, and keep the
// regex filter optional. Returns error for unknown service names.
func buildLogQL(svc, projectID, search string) (string, error) {
	var selector string
	switch svc {
	case "auth", "graphql", "provisioning":
		selector = fmt.Sprintf(`{namespace="excalibase-platform",app=%q}`, svc)
	case "watcher":
		// Watcher pods live in per-project namespaces; require projectId.
		if projectID == "" {
			return "", fmt.Errorf("service=watcher requires projectId")
		}
		selector = fmt.Sprintf(`{namespace=~".*-%s",app="excalibase-watcher"}`, escapeLokiValue(projectID))
	case "deno":
		if projectID == "" {
			return "", fmt.Errorf("service=deno requires projectId")
		}
		selector = fmt.Sprintf(`{namespace=~".*-%s",app="deno-runtime"}`, escapeLokiValue(projectID))
	case "cnpg":
		if projectID == "" {
			return "", fmt.Errorf("service=cnpg requires projectId")
		}
		selector = fmt.Sprintf(`{namespace=~".*-%s"} |~ "postgres"`, escapeLokiValue(projectID))
	case "all":
		selector = `{namespace=~"excalibase-platform|.*-proj-.*"}`
	default:
		return "", fmt.Errorf("unknown service: %s (try auth|graphql|provisioning|watcher|deno|cnpg|all)", svc)
	}
	if search != "" {
		if err := validateLogSearch(search); err != nil {
			return "", err
		}
		selector += " |~ " + lokiQuote(search)
	}
	return selector, nil
}

// maxLogSearchLen caps the user-supplied |~ regex. A bound keeps a platform
// admin from pasting a pathological/catastrophic-backtracking pattern, and
// keeps the filter to a genuine substring/regex search rather than a probe.
const maxLogSearchLen = 200

// validateLogSearch rejects empty-after-trim and trivially-broad patterns. The
// /logs proxy is designed for targeted substring/regex searches; a bare `.*` /
// `.+` (optionally anchored) matches every line, turning the proxy into a
// cluster-wide credential-harvesting scanner. Normal searches pass through.
func validateLogSearch(search string) error {
	if len(search) > maxLogSearchLen {
		return fmt.Errorf("q too long: %d chars (max %d)", len(search), maxLogSearchLen)
	}
	trimmed := strings.TrimSpace(search)
	if trimmed == "" {
		return fmt.Errorf("q must not be blank")
	}
	// Strip Loki regex anchors before testing for a match-everything pattern,
	// so `^.*$`, `.*`, `.+`, `(.*)`, etc. are all caught.
	bare := strings.Trim(trimmed, "^$()")
	switch bare {
	case ".*", ".+", ".", "", "(.*)", "(.+)":
		return fmt.Errorf("q is too broad: %q matches every log line", search)
	}
	return nil
}

// escapeLokiValue rejects characters that could break out of the Loki label
// matcher value position. projectId/orgSlug should already be opaque
// `proj-*` / `[a-z0-9-]+` strings, but defense-in-depth.
func escapeLokiValue(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return -1
	}, s)
}

func lokiQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// auditFireAndForget logs to the audit table on a best-effort basis. Failures
// do NOT roll back the admin action — the action already happened, losing
// the audit row is preferable to making operator workflows fragile.
func (h *AdminHandler) auditFireAndForget(r *http.Request, e *domain.AuditEntry) {
	if h.auditLog == nil {
		return
	}
	if e.IPAddress == "" {
		e.IPAddress = clientIP(r)
	}
	now := time.Now()
	e.Timestamp = &now
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = h.auditLog.LogAudit(ctx, e)
	}()
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if i := strings.Index(v, ","); i > 0 {
			return strings.TrimSpace(v[:i])
		}
		return strings.TrimSpace(v)
	}
	return r.RemoteAddr
}
