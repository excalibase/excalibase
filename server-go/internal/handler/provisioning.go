package handler

import (
	"encoding/json"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

type ProvisioningHandler struct {
	svc *service.ProvisioningService
}

func NewProvisioningHandler(svc *service.ProvisioningService) *ProvisioningHandler {
	return &ProvisioningHandler{svc: svc}
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
	})
}

func (h *ProvisioningHandler) ListInstances(w http.ResponseWriter, r *http.Request) {
	instances, err := h.svc.GetAllInstances()
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, instances)
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
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}

	writeJSON(w, resp)
}

func (h *ProvisioningHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	inst, err := h.svc.GetInstance(projectID)
	if err != nil {
		httpError(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, inst)
}

func (h *ProvisioningHandler) Delete(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	if err := h.svc.Deprovision(r.Context(), projectID); err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Database instance deleted successfully"))
}

func (h *ProvisioningHandler) GetCredentials(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	creds, err := h.svc.GetCredentials(projectID)
	if err != nil {
		httpError(w, err.Error(), http.StatusNotFound)
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
		httpError(w, err.Error(), http.StatusBadRequest)
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

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error":  msg,
		"status": code,
	})
}
