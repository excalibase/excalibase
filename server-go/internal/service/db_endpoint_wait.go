package service

import (
	"context"
	"fmt"
	"time"
)

// awaitServiceGone polls until the deleted Service is no longer there, so a
// port is never recorded closed while it may still answer.
func (s *DBEndpointService) awaitServiceGone(ctx context.Context, namespace, name string) error {
	deadline := time.Now().Add(s.goneTimeout)
	for {
		exists, err := s.kube.PublicDBServiceExists(ctx, namespace, name)
		if err != nil {
			return fmt.Errorf("confirm database endpoint withdrawal: %w", err)
		}
		if !exists {
			return nil
		}
		if time.Now().After(deadline) {
			return ErrDBEndpointNotObserved
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.gonePoll):
		}
	}
}
