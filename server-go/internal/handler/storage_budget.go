package handler

import (
	"context"
	"log"
	"net/http"
	"time"

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

// storageReadTimeout bounds the cluster read behind the report, so a storage
// layer that never answers is a 503 the Studio can show, not a spinner.
var storageReadTimeout = 10 * time.Second

func (h *StorageBudgetHandler) Report(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), storageReadTimeout)
	defer cancel()
	report, err := h.budget.Report(ctx)
	if err != nil {
		log.Printf("storage budget: %v", err)
		httpError(w, "the platform's storage could not be read", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, report)
}
