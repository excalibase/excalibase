package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

// TierHandler exposes the platform's tier-config table. Listing is available to
// any platform role (the admin parent route applies PermViewAny); editing
// requires PermManageSetup (platform_admin) so only admins can resize tiers.
type TierHandler struct {
	store storage.TierConfigStore
}

func NewTierHandler(store storage.TierConfigStore) *TierHandler {
	return &TierHandler{store: store}
}

// Routes mounts the tier-config endpoints (intended under /api/admin/tiers).
func (h *TierHandler) Routes(r chi.Router) {
	r.Get("/", h.List)
	r.With(auth.RequirePermission(auth.PermManageSetup)).Put("/{tier}", h.Update)
}

type tierConfigDTO struct {
	Tier          domain.TierType `json:"tier"`
	MaxProjects   int             `json:"maxProjects"`
	Instances     int             `json:"instances"`
	StorageSize   string          `json:"storageSize"`
	Memory        string          `json:"memory"`
	CPU           string          `json:"cpu"`
	BackupEnabled bool            `json:"backupEnabled"`
	// AutoPauseAfterDays: idle days before an ACTIVE project is auto-paused
	// (warning one day earlier). 0 = never.
	AutoPauseAfterDays int `json:"autoPauseAfterDays"`
}

func toTierDTO(tier domain.TierType, tc config.TierConfig) tierConfigDTO {
	return tierConfigDTO{
		Tier:          tier,
		MaxProjects:   tc.MaxProjects,
		Instances:     tc.Instances,
		StorageSize:   tc.StorageSize,
		Memory:        tc.Memory,
		CPU:           tc.CPU,
		BackupEnabled: tc.BackupEnabled,

		AutoPauseAfterDays: tc.AutoPauseAfterDays,
	}
}

// List returns every configured tier.
func (h *TierHandler) List(w http.ResponseWriter, r *http.Request) {
	m, err := h.store.ListTierConfigs(r.Context())
	if err != nil {
		httpError(w, "list tiers: "+safeError(err), http.StatusInternalServerError)
		return
	}
	out := make([]tierConfigDTO, 0, len(m))
	for tier, tc := range m {
		out = append(out, toTierDTO(tier, tc))
	}
	writeJSON(w, out)
}

// Update upserts the spec for one tier. The tier is taken from the URL, not the
// body, so a typo can't create a phantom tier.
func (h *TierHandler) Update(w http.ResponseWriter, r *http.Request) {
	tier := domain.TierType(chi.URLParam(r, "tier"))
	if !domain.IsValidTier(tier) {
		httpError(w, "unknown tier: "+string(tier), http.StatusBadRequest)
		return
	}
	var dto tierConfigDTO
	if err := json.NewDecoder(r.Body).Decode(&dto); err != nil {
		httpError(w, "bad request body: "+safeError(err), http.StatusBadRequest)
		return
	}
	tc := config.TierConfig{
		MaxProjects:   dto.MaxProjects,
		Instances:     dto.Instances,
		StorageSize:   dto.StorageSize,
		Memory:        dto.Memory,
		CPU:           dto.CPU,
		BackupEnabled: dto.BackupEnabled,

		AutoPauseAfterDays: dto.AutoPauseAfterDays,
	}
	if err := validateTierConfig(tc); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	if err := h.store.UpsertTierConfig(r.Context(), tier, tc); err != nil {
		httpError(w, "update tier: "+safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, toTierDTO(tier, tc))
}

// validateTierConfig rejects specs that would produce an invalid CNPG cluster.
// Quantity strings (cpu/memory/storage) are required and non-empty; instances
// must be at least 1; maxProjects and autoPauseAfterDays are non-negative (0 =
// unlimited / never).
func validateTierConfig(tc config.TierConfig) error {
	if tc.Instances < 1 {
		return errors.New("instances must be >= 1")
	}
	if tc.MaxProjects < 0 {
		return errors.New("maxProjects must be >= 0")
	}
	if tc.AutoPauseAfterDays < 0 {
		return errors.New("autoPauseAfterDays must be >= 0 (0 = never)")
	}
	if tc.CPU == "" || tc.Memory == "" || tc.StorageSize == "" {
		return errors.New("cpu, memory and storageSize are required")
	}
	return nil
}
