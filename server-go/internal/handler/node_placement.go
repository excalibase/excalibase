package handler

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// NodePlacement refuses a plan whose database instances cannot each get their
// own node. *service.ProvisioningService implements it.
type NodePlacement interface {
	RequireNodesForTier(ctx context.Context, tier domain.TierType) error
	RequireNodeCount(ctx context.Context, tier domain.TierType, instances int) error
}

// errNodePlacementUnchecked refuses a plan change nothing could check.
const errNodePlacementUnchecked = "the platform's nodes could not be checked for this plan"

// writeNodePlacementError answers a node refusal and reports whether it wrote
// the response: too few nodes is the caller's conflict (409), an unreadable
// cluster is ours (503, details logged only).
func writeNodePlacementError(w http.ResponseWriter, err error) bool {
	var tooFew *service.NotEnoughNodesError
	switch {
	case errors.As(err, &tooFew):
		httpError(w, tooFew.Error(), http.StatusConflict)
	case errors.Is(err, service.ErrNodePlacementUnknown):
		log.Printf("node placement refused: %v", err)
		httpError(w, service.ErrNodePlacementUnknown.Error(), http.StatusServiceUnavailable)
	default:
		return false
	}
	return true
}
