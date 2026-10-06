package mcpserver

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	probeEvery     = 2 * time.Second
	probeBurst     = 10
	maxProbeBucket = 10000
)

// probeLimits paces test_api_request per user: every probe leaves from the
// platform's own address, so one user's loop must not spend the data plane's
// per-address allowance for everyone.
type probeLimits struct {
	mu     sync.Mutex
	byUser map[string]*rate.Limiter
}

func newProbeLimits() *probeLimits {
	return &probeLimits{byUser: map[string]*rate.Limiter{}}
}

func (p *probeLimits) allow(userID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	limiter, ok := p.byUser[userID]
	if !ok {
		if len(p.byUser) >= maxProbeBucket {
			p.byUser = map[string]*rate.Limiter{}
		}
		limiter = rate.NewLimiter(rate.Every(probeEvery), probeBurst)
		p.byUser[userID] = limiter
	}
	return limiter.Allow()
}
