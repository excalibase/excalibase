package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/api/resource"
)

// TierHandler exposes the platform's tier-config table. Listing is available to
// any platform role (the admin parent route applies PermViewAny); editing
// requires PermManageSetup (platform_admin) so only admins can resize tiers.
type TierHandler struct {
	store storage.TierConfigStore
	// nodes checks a multi-instance spec fits the platform; such a spec is
	// refused while it is unset.
	nodes NodePlacement
	// planChanged is told of every edit; it must not block the response.
	planChanged func()
}

// SetNodePlacement wires the check a multi-instance spec must pass.
func (h *TierHandler) SetNodePlacement(nodes NodePlacement) { h.nodes = nodes }

func NewTierHandler(store storage.TierConfigStore) *TierHandler {
	return &TierHandler{store: store}
}

// SetPlanChangeHook runs after a plan is edited: an edit that lowers a cap
// re-runs the app disk rule on the apps it now caps (EXC-523).
func (h *TierHandler) SetPlanChangeHook(hook func()) { h.planChanged = hook }

// Routes mounts the tier-config endpoints (intended under /api/admin/tiers).
func (h *TierHandler) Routes(r chi.Router) {
	r.Get("/", h.List)
	r.With(auth.RequirePermission(auth.PermManageSetup)).Put("/{tier}", h.Update)
}

type tierConfigDTO struct {
	Tier        domain.TierType `json:"tier"`
	MaxProjects int             `json:"maxProjects"`
	Instances   int             `json:"instances"`
	StorageSize string          `json:"storageSize"`
	// MaxStorageSize is the disk a project may grow to (EXC-492).
	MaxStorageSize string `json:"maxStorageSize"`
	// MaxAppDiskSize is the largest disk one app may have (EXC-523).
	MaxAppDiskSize string `json:"maxAppDiskSize"`
	// MaxApps is how many apps one project may hold (EXC-524); required, so
	// an omitted field is never read as "no apps".
	MaxApps       *int   `json:"maxApps"`
	Memory        string `json:"memory"`
	CPU           string `json:"cpu"`
	BackupEnabled bool   `json:"backupEnabled"`
	// AutoPauseAfterDays: idle days before an ACTIVE project is auto-paused
	// (warning one day earlier). 0 = never.
	AutoPauseAfterDays int `json:"autoPauseAfterDays"`
}

func toTierDTO(tier domain.TierType, tc config.TierConfig) tierConfigDTO {
	return tierConfigDTO{
		Tier:           tier,
		MaxProjects:    tc.MaxProjects,
		Instances:      tc.Instances,
		StorageSize:    tc.StorageSize,
		MaxStorageSize: tc.MaxStorageSize,
		MaxAppDiskSize: tc.MaxAppDiskSize,
		MaxApps:        &tc.MaxApps,
		Memory:         tc.Memory,
		CPU:            tc.CPU,
		BackupEnabled:  tc.BackupEnabled,

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
		MaxProjects:    dto.MaxProjects,
		Instances:      dto.Instances,
		StorageSize:    dto.StorageSize,
		MaxStorageSize: dto.MaxStorageSize,
		MaxAppDiskSize: dto.MaxAppDiskSize,
		MaxApps:        requiredCount(dto.MaxApps),
		Memory:         dto.Memory,
		CPU:            dto.CPU,
		BackupEnabled:  dto.BackupEnabled,

		AutoPauseAfterDays: dto.AutoPauseAfterDays,
	}
	if err := validateTierConfig(tc); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	if !h.instancesFitThePlatform(w, r, tier, tc.Instances) {
		return
	}
	if err := h.store.UpsertTierConfig(r.Context(), tier, tc); err != nil {
		httpError(w, "update tier: "+safeError(err), http.StatusInternalServerError)
		return
	}
	if h.planChanged != nil {
		h.planChanged()
	}
	writeJSON(w, toTierDTO(tier, tc))
}

// requiredCount reads an omitted count as -1, which validation refuses, so an
// absent field is never taken as zero.
func requiredCount(value *int) int {
	if value == nil {
		return -1
	}
	return *value
}

// maxAppsPerProjectCeiling bounds an admin's edit; a project's namespace quota
// and capacity admission still decide what actually runs.
const maxAppsPerProjectCeiling = 1000

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
	if _, err := tc.SlotWALKeepSize(); err != nil {
		return errors.New("storageSize must be a storage quantity such as 5Gi")
	}
	if err := validateMaxStorage(tc); err != nil {
		return err
	}
	if tc.MaxApps < 0 || tc.MaxApps > maxAppsPerProjectCeiling {
		return fmt.Errorf("maxApps is required, between 0 and %d (0 offers no apps)", maxAppsPerProjectCeiling)
	}
	if _, err := apphost.PlanDiskBytes(tc.MaxAppDiskSize); err != nil {
		return errors.New("maxAppDiskSize must be a whole number of gibibytes such as 20Gi (0Gi offers no app disks)")
	}
	return nil
}

// validateMaxStorage requires the disk a project may grow to, no smaller than
// the disk it starts with.
func validateMaxStorage(tc config.TierConfig) error {
	limit, err := resource.ParseQuantity(tc.MaxStorageSize)
	if err != nil {
		return errors.New("maxStorageSize must be a storage quantity such as 500Gi")
	}
	start, err := resource.ParseQuantity(tc.StorageSize)
	if err != nil || limit.Cmp(start) < 0 {
		return errors.New("maxStorageSize must be at least storageSize")
	}
	return nil
}

// instancesFitThePlatform refuses a spec with more instances than the
// platform has nodes for, and reports whether it may be stored.
func (h *TierHandler) instancesFitThePlatform(w http.ResponseWriter, r *http.Request, tier domain.TierType, instances int) bool {
	if instances <= 1 {
		return true
	}
	if h.nodes == nil {
		httpError(w, errNodePlacementUnchecked, http.StatusServiceUnavailable)
		return false
	}
	err := h.nodes.RequireNodeCount(r.Context(), tier, instances)
	if err == nil {
		return true
	}
	if !writeNodePlacementError(w, err) {
		log.Printf("tier update refused: %v", err)
		httpError(w, errNodePlacementUnchecked, http.StatusServiceUnavailable)
	}
	return false
}
