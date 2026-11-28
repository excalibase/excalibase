package creditmeter

import (
	"context"
	"log"
	"time"
)

// Leader says whether this replica is the one that samples and meters.
type Leader interface {
	IsLeader(ctx context.Context) (bool, error)
}

// Runner is the leader-only loop that samples usage and bills settled hours.
type Runner struct {
	leader Leader
	sample func(context.Context) error
	meter  func(context.Context) error
}

func NewRunner(leader Leader, sample, meter func(context.Context) error) *Runner {
	return &Runner{leader: leader, sample: sample, meter: meter}
}

// Tick is one round: on the leading replica, sample, then bill what has settled.
func (r *Runner) Tick(ctx context.Context) {
	leading, err := r.leader.IsLeader(ctx)
	if err != nil {
		log.Printf("credit meter: leadership check: %v", err)
		return
	}
	if !leading {
		return
	}
	if err := r.sample(ctx); err != nil {
		log.Printf("credit meter: sample: %v", err)
	}
	if err := r.meter(ctx); err != nil {
		log.Printf("credit meter: bill: %v", err)
	}
}

// Start runs Tick at a random point of every slot and returns the function that stops it.
func (r *Runner) Start(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		for {
			timer := time.NewTimer(NextDelay(time.Now()))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			r.Tick(ctx)
		}
	}()
	return cancel
}
