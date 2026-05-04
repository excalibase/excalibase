package service

import (
	"context"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// WalLagAdvertiser is the optional sub-interface BackupAdapters
// implement when they can report continuous-archive lag. Docker
// adapter (Phase 2 + WAL-G) supports it; K8s and BYOC do not.
type WalLagAdvertiser interface {
	WalLag(ctx context.Context, inst *domain.DatabaseInstance) (WalLagInfo, error)
}

// WalLagInfo summarises the most recent WAL push.
type WalLagInfo struct {
	// SecondsSinceLastSuccess is how long ago wal-g last successfully
	// pushed a WAL segment. -1 means "no data yet" — there has never
	// been a successful push.
	SecondsSinceLastSuccess int64 `json:"secondsSinceLastSuccess"`
	// LastSuccess is the RFC3339 timestamp of the last successful
	// push, or empty if there has never been one.
	LastSuccess string `json:"lastSuccess,omitempty"`
	// Error is non-empty if probing the lag itself failed (sidecar
	// unreachable, etc.) — distinguished from "no data yet".
	Error string `json:"error,omitempty"`
}

// GetWalLag is a typed alias the handler calls. Returns 501-shaped
// info for adapters that don't implement WalLagAdvertiser so the
// HTTP layer can map cleanly.
func (s *BackupService) GetWalLag(ctx context.Context, projectID string) (WalLagInfo, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return WalLagInfo{}, fmt.Errorf("project not found: %s", projectID)
	}
	adapter, err := resolveAdapter(s.adapters, inst)
	if err != nil {
		return WalLagInfo{}, err
	}
	advertiser, ok := adapter.(WalLagAdvertiser)
	if !ok {
		return WalLagInfo{}, ErrWalLagUnsupported
	}
	return advertiser.WalLag(ctx, inst)
}

// ErrWalLagUnsupported is returned by GetWalLag when the adapter
// doesn't implement WalLagAdvertiser. K8s and BYOC adapters fall
// here; the HTTP handler maps it to 501 Not Implemented.
var ErrWalLagUnsupported = fmt.Errorf("wal-lag not supported for this deployment mode")

// staticWalLag is a tiny helper used by tests/adapters that want
// to surface a fixed lag value derived from a "last success"
// timestamp. Production adapters (Docker WAL-G) probe the
// sidecar's heartbeat / archive history.
func staticWalLag(lastSuccess time.Time) WalLagInfo {
	if lastSuccess.IsZero() {
		return WalLagInfo{SecondsSinceLastSuccess: -1}
	}
	return WalLagInfo{
		SecondsSinceLastSuccess: int64(time.Since(lastSuccess).Seconds()),
		LastSuccess:             lastSuccess.UTC().Format(time.RFC3339),
	}
}
