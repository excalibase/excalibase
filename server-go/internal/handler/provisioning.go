package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

// backupDisposition renders the deprovision choice as a fixed literal for logs,
// so request-derived data never reaches the log line.
func backupDisposition(opts service.DeprovisionOptions) string {
	if opts.DeleteBackups {
		return "purge"
	}
	return "keep"
}

type ProvisioningHandler struct {
	svc       *service.ProvisioningService
	orgStore  storage.OrgStore
	pauseSvc  *service.PauseService // optional; nil → /pause + /resume return 503
	instances storage.InstanceStore
	// activity supplies lastSeenAt for GET / list; nil → field omitted.
	activity storage.ProjectActivityStore
	// corsStore backs /cors and the corsAllowedOrigins field on /info;
	// nil → /cors returns 503 and /info reports no origins.
	corsStore storage.ProjectCorsStore
	// authSettingsStore backs /auth-settings and the requireEmailVerification
	// + siteUrl fields on /info; nil → /auth-settings returns 503 and /info
	// reports the zero value.
	authSettingsStore storage.ProjectAuthSettingsStore
}

func NewProvisioningHandler(svc *service.ProvisioningService, orgStore storage.OrgStore) *ProvisioningHandler {
	return &ProvisioningHandler{svc: svc, orgStore: orgStore}
}

// SetPauseService wires the pause/resume backend. Wired post-construction
// because pauseService depends on backupSvc which is built after provHandler.
func (h *ProvisioningHandler) SetPauseService(s *service.PauseService) { h.pauseSvc = s }

// SetInstanceStore lets the pause handlers look up the post-transition
// state for the response shape.
func (h *ProvisioningHandler) SetInstanceStore(s storage.InstanceStore) { h.instances = s }

// SetAuthSettingsStore wires the per-project auth settings store (EXC-367).
func (h *ProvisioningHandler) SetAuthSettingsStore(s storage.ProjectAuthSettingsStore) {
	h.authSettingsStore = s
}

func (h *ProvisioningHandler) Routes(r chi.Router) {
	r.Get("/", h.ListInstances)
	r.Post("/", h.Provision)
	r.Post("/estimate", h.EstimateCost)
	r.Route("/{projectId}", func(r chi.Router) {
		r.Get("/", h.GetStatus)
		r.Delete("/", h.Delete)
		r.Get("/credentials", h.GetCredentials)
		r.Patch("/deletion-protection", h.SetDeletionProtection)
		r.Post("/backups/purge", h.PurgeBackups)
		r.Post("/pause", h.Pause)
		r.Post("/resume", h.Resume)
	})
}

// Pause stops the project workload after taking a backup. Body:
//
//	{"reason": "manual"}     // optional; defaults to manual
//
// 503 when pause service isn't wired, 400 for a deployment mode with no
// pauser, 404 for missing project, 500 on unexpected failures.
func (h *ProvisioningHandler) Pause(w http.ResponseWriter, r *http.Request) {
	if h.pauseSvc == nil {
		httpError(w, "pause service not configured", http.StatusServiceUnavailable)
		return
	}
	projectID := chi.URLParam(r, "projectId")
	if h.instances != nil {
		if inst, _ := h.instances.FindByProjectID(projectID); inst == nil {
			httpError(w, "project not found", http.StatusNotFound)
			return
		}
	}
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Reason == "" {
		body.Reason = domain.PauseReasonManual
	}
	if err := h.pauseSvc.Pause(r.Context(), projectID, body.Reason); err != nil {
		if errors.Is(err, service.ErrPauseUnsupported) {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	if h.instances != nil {
		if got, _ := h.instances.FindByProjectID(projectID); got != nil {
			writeJSON(w, map[string]interface{}{
				"projectId": got.ProjectID, "status": got.Status, "pauseReason": got.PauseReason,
			})
			return
		}
	}
	writeJSON(w, map[string]string{"status": "PAUSED"})
}

// Resume restarts a paused project's workload + clears PauseReason.
func (h *ProvisioningHandler) Resume(w http.ResponseWriter, r *http.Request) {
	if h.pauseSvc == nil {
		httpError(w, "pause service not configured", http.StatusServiceUnavailable)
		return
	}
	projectID := chi.URLParam(r, "projectId")
	if h.instances != nil {
		if inst, _ := h.instances.FindByProjectID(projectID); inst == nil {
			httpError(w, "project not found", http.StatusNotFound)
			return
		}
	}
	if err := h.pauseSvc.Resume(r.Context(), projectID); err != nil {
		if errors.Is(err, service.ErrPauseUnsupported) {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	if h.instances != nil {
		if got, _ := h.instances.FindByProjectID(projectID); got != nil {
			writeJSON(w, map[string]interface{}{
				"projectId": got.ProjectID, "status": got.Status,
			})
			return
		}
	}
	writeJSON(w, map[string]string{"status": "ACTIVE"})
}

func (h *ProvisioningHandler) ListInstances(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())

	// Fail closed when the org store isn't wired: without it we cannot scope
	// instances to the caller's orgs, and returning the unfiltered all-tenant
	// list would leak every tenant's instances. 503 signals a misconfigured
	// deployment rather than silently dumping everything.
	if h.orgStore == nil {
		httpError(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}

	allInstances, err := h.svc.GetAllInstances()
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}

	// Platform admins (PermViewAny) see everything. An unauthenticated request
	// that reached this far also sees the unscoped list — but in practice this
	// route is mounted behind RequireAuth, so user is non-nil; the nil-guard is
	// retained only as defense against a misconfigured mount.
	if user == nil || auth.HasPermission(user.Role, auth.PermViewAny) {
		writeJSON(w, h.projectListResponse(r.Context(), allInstances))
		return
	}

	// Regular users see only instances belonging to their orgs
	myOrgs, _ := h.orgStore.FindOrgsByUser(r.Context(), user.ID)
	orgIDs := make(map[string]bool, len(myOrgs))
	for _, org := range myOrgs {
		orgIDs[org.ID] = true
	}

	var filtered []*domain.DatabaseInstance
	for _, inst := range allInstances {
		if orgIDs[inst.OrgID] {
			filtered = append(filtered, inst)
		}
	}
	if filtered == nil {
		filtered = []*domain.DatabaseInstance{}
	}
	writeJSON(w, h.projectListResponse(r.Context(), filtered))
}

func (h *ProvisioningHandler) Provision(w http.ResponseWriter, r *http.Request) {
	var req domain.ProvisioningRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidRequestBody, http.StatusBadRequest)
		return
	}

	// Set owner from authenticated user
	user := auth.GetUser(r.Context())
	if user == nil {
		httpError(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	req.OwnerID = user.ID

	// Creating a project requires the create_project org permission (Owner or
	// Admin) in the TARGET org — so a Developer/Viewer in someone else's org
	// can't spin up projects there. A user creating in their own freshly-made
	// org is its Owner, so self-serve stays open. platform_admin bypasses.
	if !auth.HasPermission(user.Role, auth.PermManageUsers) {
		if req.OrgID == "" || h.orgStore == nil {
			httpError(w, "org is required to create a project", http.StatusBadRequest)
			return
		}
		member, merr := h.orgStore.GetOrgMember(r.Context(), req.OrgID, user.ID)
		if merr != nil || member == nil || !auth.HasOrgPermission(member.Role, auth.OrgPermCreateProject) {
			httpError(w, "insufficient org role to create a project (owner/admin required)", http.StatusForbidden)
			return
		}
	}

	resp, err := h.svc.Provision(r.Context(), req)
	if err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}

	writeJSON(w, resp)
}

func (h *ProvisioningHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	inst, err := h.svc.GetInstance(projectID)
	if err != nil {
		httpError(w, safeError(err), http.StatusNotFound)
		return
	}
	writeJSON(w, h.projectResponse(r.Context(), inst))
}

// deprovisionBody is the optional JSON body of DELETE /{projectId}.
type deprovisionBody struct {
	// ConfirmDeleteBackups also deletes every backup object under the
	// project's prefix once the project is gone. Absent/false keeps them.
	ConfirmDeleteBackups bool `json:"confirmDeleteBackups"`
}

// decodeDeprovisionOptions reads the optional deprovision body. An empty
// body is the safe default (keep backups); malformed JSON is an error so a
// typo can never silently turn into "keep" or "delete".
func decodeDeprovisionOptions(r *http.Request) (service.DeprovisionOptions, error) {
	var body deprovisionBody
	if r.Body == nil {
		return service.DeprovisionOptions{}, nil
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		return service.DeprovisionOptions{}, err
	}
	return service.DeprovisionOptions{DeleteBackups: body.ConfirmDeleteBackups}, nil
}

func (h *ProvisioningHandler) Delete(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	tenant, _ := custommw.TenantIDFromContext(r.Context())
	opts, err := decodeDeprovisionOptions(r)
	if err != nil {
		httpError(w, errInvalidRequestBody, http.StatusBadRequest)
		return
	}
	log.Printf("tenant=%s action=deprovision path=%s backups=%s", tenant, r.URL.Path, backupDisposition(opts))
	if err := h.svc.DeprovisionWithOptions(r.Context(), projectID, opts); err != nil {
		log.Printf("tenant=%s action=deprovision status=failed err=%v", tenant, err)
		writeDeprovisionError(w, err)
		return
	}
	log.Printf("tenant=%s action=deprovision status=ok", tenant)
	writeJSON(w, map[string]string{"projectId": projectID, "status": "DELETED"})
}

// writeDeprovisionError separates a refused request — the caller asked for
// something the platform will not do — from a teardown that started and did
// not finish. The latter is a 500 with a fixed message: the underlying cause
// is a cluster/vault detail that belongs in the server log, while the row
// keeps the failing step so an operator can see it through GET.
func writeDeprovisionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrProjectNotFound):
		httpError(w, "project not found", http.StatusNotFound)
	case errors.Is(err, service.ErrBackupPurgeNotConfigured):
		httpError(w, safeError(err), http.StatusBadRequest)
	case errors.Is(err, service.ErrDeletionProtected):
		httpError(w, safeError(err), http.StatusBadRequest)
	default:
		httpError(w, "deletion did not complete; the project remains in DELETING — retry the request",
			http.StatusInternalServerError)
	}
}

// PurgeBackups retries the backup deletion of a project whose deprovision
// left it in BACKUPS_PENDING_DELETE. 409 for a live project, 404 when the
// row is gone (nothing left to retry), 503 when no purger is wired.
func (h *ProvisioningHandler) PurgeBackups(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	deleted, err := h.svc.PurgeBackups(r.Context(), projectID)
	switch {
	case errors.Is(err, service.ErrProjectNotFound):
		httpError(w, safeError(err), http.StatusNotFound)
	case errors.Is(err, service.ErrBackupsNotPendingDelete):
		httpError(w, safeError(err), http.StatusConflict)
	case errors.Is(err, service.ErrBackupPurgeNotConfigured):
		httpError(w, safeError(err), http.StatusServiceUnavailable)
	case err != nil:
		log.Printf("action=purge_backups project=%s status=failed err=%v", projectID, err)
		httpError(w, "backup purge failed; project stays pending, retry later", http.StatusInternalServerError)
	default:
		writeJSON(w, map[string]interface{}{"projectId": projectID, "status": "purged", "deletedObjects": deleted})
	}
}

func (h *ProvisioningHandler) GetCredentials(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	creds, err := h.svc.GetCredentials(projectID)
	if errors.Is(err, service.ErrProjectDeleting) {
		httpError(w, safeError(err), http.StatusConflict)
		return
	}
	if err != nil {
		httpError(w, safeError(err), http.StatusNotFound)
		return
	}
	writeJSON(w, creds)
}

func (h *ProvisioningHandler) SetDeletionProtection(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	var body struct {
		Enabled bool `json:"enabled"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if err := h.svc.SetDeletionProtection(projectID, body.Enabled); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]interface{}{"projectId": projectID, "deletionProtection": body.Enabled})
}

func (h *ProvisioningHandler) EstimateCost(w http.ResponseWriter, r *http.Request) {
	var req domain.ProvisioningRequest
	json.NewDecoder(r.Body).Decode(&req)

	// Simple cost estimation
	costs := map[domain.TierType]float64{
		domain.Free:       0,
		domain.Standard:   49.99,
		domain.Enterprise: 199.99,
	}
	cost := costs[req.Tier]
	writeJSON(w, domain.CostEstimation{
		Tier:           req.Tier,
		MonthlyCostUSD: cost,
		Breakdown: map[string]float64{
			"compute": cost * 0.6,
			"storage": cost * 0.3,
			"backup":  cost * 0.1,
		},
	})
}

func (h *ProvisioningHandler) GetLogs(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	lines := 100
	if l := r.URL.Query().Get("lines"); l != "" {
		if v, err := strconv.Atoi(l); err == nil {
			lines = v
		}
	}
	if lines > 10000 {
		lines = 10000
	}
	out, err := h.svc.GetLogs(r.Context(), projectID, lines)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"logs": out})
}

func (h *ProvisioningHandler) RotateCredentials(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	creds, err := h.svc.RotateCredentials(r.Context(), projectID)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, creds)
}

func (h *ProvisioningHandler) SetMaintenanceWindow(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	var cfg domain.MaintenanceWindowConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		httpError(w, errInvalidRequestBody, http.StatusBadRequest)
		return
	}
	if err := h.svc.SetMaintenanceWindow(projectID, cfg); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]string{"status": "updated"})
}

func (h *ProvisioningHandler) GetMaintenanceWindow(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	cfg, err := h.svc.GetMaintenanceWindow(projectID)
	if err != nil {
		httpError(w, safeError(err), http.StatusNotFound)
		return
	}
	writeJSON(w, cfg)
}

// GetProjectInfo returns a flat project + org metadata blob used by the
// auth service at JWT-mint time so support staff can search by display
// names in dashboards/logs. Defers all data lookup to the service/store
// layers — no business logic here.
func (h *ProvisioningHandler) GetProjectInfo(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	if !isValidID(projectID) {
		httpError(w, "invalid projectId", http.StatusBadRequest)
		return
	}

	inst, err := h.svc.GetInstance(projectID)
	if err != nil {
		httpError(w, safeError(err), http.StatusNotFound)
		return
	}
	// The data plane mints JWTs and routes traffic from this payload. A
	// project under teardown is no longer a project it may serve, and it is
	// about to stop existing, so it reads the same as an unknown one.
	if inst.Status == string(domain.StatusDeleting) {
		httpError(w, "project not found", http.StatusNotFound)
		return
	}

	org, ok := h.resolveInfoOrg(w, r, inst.OrgID)
	if !ok {
		return
	}

	// The auth service resolves email-verification + redirect behavior from
	// these fields and caches it; a read failure is a 503 so it keeps its
	// last good settings rather than a wrong default.
	authSettings, aerr := h.authSettingsForInfo(r.Context(), inst.ProjectID)
	if aerr != nil {
		httpError(w, "auth settings unavailable", http.StatusServiceUnavailable)
		return
	}

	// The data plane resolves CORS from this field and caches it; a read
	// failure is a 503 so it keeps its last good list rather than a deny.
	corsOrigins, cerr := h.corsOriginsForInfo(r.Context(), inst.ProjectID)
	if cerr != nil {
		httpError(w, "cors allowlist unavailable", http.StatusServiceUnavailable)
		return
	}

	info := domain.ProjectInfo{
		ProjectID:                inst.ProjectID,
		ProjectName:              inst.ProjectName,
		OrgID:                    inst.OrgID,
		OrgSlug:                  org.Slug,
		OrgName:                  org.Name,
		RealtimeAutoEnable:       true, // v1: hardcoded; per-project override is a future column on instances
		CorsAllowedOrigins:       corsOrigins,
		RequireEmailVerification: authSettings.RequireEmailVerification,
		SiteURL:                  authSettings.SiteURL,
	}

	writeJSON(w, info)
}

// resolveInfoOrg looks up the org metadata GetProjectInfo embeds in its
// response, writing the error response itself when it can't. Refuses to
// forge a slug from the raw org id — the auth service mints JWTs from this
// payload and a UUID-as-slug feeds downstream verification with the wrong
// `iss` claim, so any lookup failure is a 503 telling the caller to retry
// rather than caching a poisoned value.
func (h *ProvisioningHandler) resolveInfoOrg(w http.ResponseWriter, r *http.Request, orgID string) (*domain.Org, bool) {
	if h.orgStore == nil {
		httpError(w, "org store not configured", http.StatusServiceUnavailable)
		return nil, false
	}
	if orgID == "" {
		httpError(w, "project has no org assignment", http.StatusServiceUnavailable)
		return nil, false
	}
	org, err := h.orgStore.FindOrgByID(r.Context(), orgID)
	if err != nil || org == nil {
		httpError(w, "org metadata unavailable", http.StatusServiceUnavailable)
		return nil, false
	}
	return org, true
}
