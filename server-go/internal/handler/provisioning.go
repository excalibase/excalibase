package handler

import (
	"encoding/json"
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

type ProvisioningHandler struct {
	svc      *service.ProvisioningService
	orgStore storage.OrgStore
}

func NewProvisioningHandler(svc *service.ProvisioningService, orgStore storage.OrgStore) *ProvisioningHandler {
	return &ProvisioningHandler{svc: svc, orgStore: orgStore}
}

func (h *ProvisioningHandler) Routes(r chi.Router) {
	r.Get("/", h.ListInstances)
	r.Post("/", h.Provision)
	r.Post("/estimate", h.EstimateCost)
	r.Post("/byoc", h.ProvisionBYOC)
	r.Route("/{projectId}", func(r chi.Router) {
		r.Get("/", h.GetStatus)
		r.Delete("/", h.Delete)
		r.Get("/credentials", h.GetCredentials)
		r.Patch("/deletion-protection", h.SetDeletionProtection)
	})
}

func (h *ProvisioningHandler) ListInstances(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())

	allInstances, err := h.svc.GetAllInstances()
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}

	// Platform admins see everything; also fallback if no orgStore or no user context
	if user == nil || h.orgStore == nil || auth.HasPermission(user.Role, auth.PermViewAny) {
		writeJSON(w, allInstances)
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
	writeJSON(w, filtered)
}

func (h *ProvisioningHandler) Provision(w http.ResponseWriter, r *http.Request) {
	var req domain.ProvisioningRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Set owner from authenticated user
	if user := auth.GetUser(r.Context()); user != nil {
		req.OwnerID = user.ID
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
	writeJSON(w, inst)
}

func (h *ProvisioningHandler) Delete(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	tenant, _ := custommw.TenantIDFromContext(r.Context())
	log.Printf("tenant=%s action=deprovision path=%s", tenant, r.URL.Path)
	if err := h.svc.Deprovision(r.Context(), projectID); err != nil {
		log.Printf("tenant=%s action=deprovision status=failed err=%v", tenant, err)
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	log.Printf("tenant=%s action=deprovision status=ok", tenant)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Database instance deleted successfully"))
}

func (h *ProvisioningHandler) GetCredentials(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	creds, err := h.svc.GetCredentials(projectID)
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
		httpError(w, "invalid request body", http.StatusBadRequest)
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

	info := domain.ProjectInfo{
		ProjectID:   inst.ProjectID,
		ProjectName: inst.ProjectName,
		OrgID:       inst.OrgID,
		// Fall back to the orgID for slug/name when the org record can't
		// be loaded — keeps the endpoint useful for older instances that
		// predate the org table or when orgStore isn't wired.
		OrgSlug: inst.OrgID,
		OrgName: inst.OrgID,
	}

	if h.orgStore != nil && inst.OrgID != "" {
		if org, oerr := h.orgStore.FindOrgByID(r.Context(), inst.OrgID); oerr == nil && org != nil {
			info.OrgSlug = org.Slug
			info.OrgName = org.Name
		}
	}

	writeJSON(w, info)
}

