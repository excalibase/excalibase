package handler

import (
	"log"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/storagebudget"
)

// StorageBudgetHandler shows platform admins the storage budget: every
// volume's reservation against the configured share of the node's storage.
type StorageBudgetHandler struct {
	budget *storagebudget.Budget
}

func NewStorageBudgetHandler(budget *storagebudget.Budget) *StorageBudgetHandler {
	return &StorageBudgetHandler{budget: budget}
}

func (h *StorageBudgetHandler) Report(w http.ResponseWriter, r *http.Request) {
	report, err := h.budget.Report(r.Context())
	if err != nil {
		log.Printf("storage budget: %v", err)
		httpError(w, "the platform's storage could not be read", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, report)
}
