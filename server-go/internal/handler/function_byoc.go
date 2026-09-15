package handler

import (
	"context"
	"log"
	"maps"
	"net/netip"
	"time"

	"github.com/excalibase/provisioning-poc/internal/byoc"
)

// DefaultBYOCRepinInterval bounds how stale a pinned BYOC address can be: a
// legitimate DNS change is picked up by the next re-pin.
const DefaultBYOCRepinInterval = 10 * time.Minute

// byocPin is the address a BYOC project's functions were last deployed with.
type byocPin struct {
	host string
	addr netip.Addr
}

// SetEgressGuard installs the operator-configured BYOC guard (allowlist +
// resolver). Without it the handler falls back to byoc.Default().
func (h *FunctionHandler) SetEgressGuard(g *byoc.Guard) { h.egressGuard = g }

func (h *FunctionHandler) egress() *byoc.Guard {
	if h.egressGuard != nil {
		return h.egressGuard
	}
	return byoc.Default()
}

// pinnedDBEnv resolves a BYOC host through the guard and returns the DSN the
// runtime may dial: the authority carries the validated IP, the hostname
// travels separately for TLS SNI, and BYOC_PINNED tells the runtime to refuse
// anything else. The runtime cannot dial through the guard itself, so this is
// where a rebind after registration is caught (EXC-359).
func (h *FunctionHandler) pinnedDBEnv(ctx context.Context, projectID string, target dbTarget) (map[string]string, error) {
	addr, err := h.egress().ResolvePinned(ctx, target.host, h.pinnedAddr(projectID, target.host))
	if err != nil {
		return nil, err
	}
	h.rememberPin(projectID, byocPin{host: target.host, addr: addr})
	return map[string]string{
		"EXCALIBASE_DB_URL":  target.url(addr.String()),
		"EXCALIBASE_DB_HOST": target.host,
		"BYOC_PINNED":        "1",
	}, nil
}

func (h *FunctionHandler) rememberPin(projectID string, pin byocPin) {
	h.pinMu.Lock()
	defer h.pinMu.Unlock()
	if h.pins == nil {
		h.pins = make(map[string]byocPin)
	}
	h.pins[projectID] = pin
}

// pinnedAddr is the address the project is currently deployed with, or the
// zero Addr when it has never been pinned or its host changed.
func (h *FunctionHandler) pinnedAddr(projectID, host string) netip.Addr {
	h.pinMu.Lock()
	defer h.pinMu.Unlock()
	if pin, ok := h.pins[projectID]; ok && pin.host == host {
		return pin.addr
	}
	return netip.Addr{}
}

func (h *FunctionHandler) currentPins() map[string]byocPin {
	h.pinMu.Lock()
	defer h.pinMu.Unlock()
	return maps.Clone(h.pins)
}

// RepinBYOC re-resolves every pinned BYOC host and redeploys the project's
// functions when the public address changed. A host that now resolves to an
// internal address is logged and left on its last good pin.
func (h *FunctionHandler) RepinBYOC(ctx context.Context) {
	for projectID, pin := range h.currentPins() {
		if ctx.Err() != nil {
			return
		}
		addr, err := h.egress().ResolvePinned(ctx, pin.host, pin.addr)
		if err != nil {
			log.Printf("WARN: byoc repin project=%s: %v", projectID, err)
			continue
		}
		if addr == pin.addr {
			continue
		}
		h.redeployPinned(ctx, projectID)
	}
}

// redeployPinned re-sends the project's functions through the replay path,
// which rebuilds the env (and therefore the pin) from the store.
func (h *FunctionHandler) redeployPinned(ctx context.Context, projectID string) {
	target, err := h.ReplayRuntimeFor(ctx, projectID)
	if err != nil {
		log.Printf("WARN: byoc repin project=%s: %v", projectID, err)
		return
	}
	deploys, err := h.ReplayDeploys(ctx, projectID)
	if err != nil {
		log.Printf("WARN: byoc repin project=%s: %v", projectID, err)
		return
	}
	for _, req := range deploys {
		if err := target.Deploy(ctx, req); err != nil {
			log.Printf("WARN: byoc repin %s: %v", req.ID, err)
		}
	}
	log.Printf("byoc repin: project=%s functions=%d", projectID, len(deploys))
}

// StartBYOCRepin runs RepinBYOC every interval (DefaultBYOCRepinInterval when
// non-positive) until the returned stop function is called.
func (h *FunctionHandler) StartBYOCRepin(interval time.Duration) func() {
	if interval <= 0 {
		interval = DefaultBYOCRepinInterval
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.RepinBYOC(ctx)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
