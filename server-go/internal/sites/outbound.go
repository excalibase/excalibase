package sites

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

// RouterWorker is the router script edge/sites-router/deploy.sh uploads by default; server sites need that name.
const RouterWorker = "excalibase-sites-router"

// outboundCheckTTL is how long a seen binding is trusted before it is read again.
const outboundCheckTTL = 5 * time.Minute

// ErrOutboundNotBound is a server publish refused because the router does not
// send site code's requests through the outbound Worker.
var ErrOutboundNotBound = errors.New("server sites are unavailable: the sites router does not send their requests through the outbound Worker")

// OutboundBinding reads the outbound Worker the router script's binding of the dispatch namespace names.
type OutboundBinding interface {
	DispatchOutbound(ctx context.Context, routerScript string) (string, error)
}

type outboundCheck struct {
	mu     sync.Mutex
	seenAt time.Time
}

// requireOutbound refuses to publish server code unless the router binds the
// configured outbound Worker, so no site code ever runs with unfiltered egress.
func (s *Service) requireOutbound(ctx context.Context) error {
	check := &s.outbound
	check.mu.Lock()
	defer check.mu.Unlock()
	now := s.now()
	if !check.seenAt.IsZero() && now.Sub(check.seenAt) < outboundCheckTTL {
		return nil
	}
	bound, err := s.options.Outbound.DispatchOutbound(ctx, RouterWorker)
	if err != nil {
		log.Printf("sites: read the router's outbound binding: %v", err)
		return ErrOutboundNotBound
	}
	if bound != s.options.OutboundWorker {
		log.Printf("sites: router %s binds outbound Worker %q, want %q; server publishes refused", RouterWorker, bound, s.options.OutboundWorker)
		return ErrOutboundNotBound
	}
	check.seenAt = now
	return nil
}
